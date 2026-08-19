package game

import (
	"context"
	"strings"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// AbortRoomUseCase tears a room down without playing its live round out, and
// gives every open stake back.
//
// It is the escape hatch for the two situations where a round cannot be
// settled honestly:
//
//	the process is shutting down while a betting window is open;
//	a previous process died and left the room behind (startup recovery).
//
// Aborting is deliberately not "settle with whatever we have": rolling dice
// for a window that never finished would resolve bets that other players
// never got the chance to place against. Refunding is the neutral outcome —
// nobody wins, nobody loses.
//
// Storage shape of an aborted round: the round row stays in status 'betting'
// and its bets keep won = NULL, because the schema (rightly) refuses to record
// a settlement without dice. What makes the money whole is the ledger: each
// open stake gets a "payout" entry of exactly the stake, so
// deposits + payouts - withdrawals - bets still equals the balance. An
// abandoned 'betting' round inside a closed room is therefore the marker of an
// aborted round.
type AbortRoomUseCase struct {
	tx           ports.TxManager
	rooms        ports.RoomRepository
	rounds       ports.RoundRepository
	bets         ports.BetRepository
	players      ports.PlayerRepository
	transactions ports.TransactionRepository
	state        ports.RoomStateStore
	sessions     ports.PlayerSessionStore
	ids          ports.IDGenerator
	clock        ports.Clock
}

// NewAbortRoomUseCase wires an AbortRoomUseCase from its ports.
func NewAbortRoomUseCase(
	tx ports.TxManager,
	rooms ports.RoomRepository,
	rounds ports.RoundRepository,
	bets ports.BetRepository,
	players ports.PlayerRepository,
	transactions ports.TransactionRepository,
	state ports.RoomStateStore,
	sessions ports.PlayerSessionStore,
	ids ports.IDGenerator,
	clock ports.Clock,
) *AbortRoomUseCase {
	return &AbortRoomUseCase{
		tx:           tx,
		rooms:        rooms,
		rounds:       rounds,
		bets:         bets,
		players:      players,
		transactions: transactions,
		state:        state,
		sessions:     sessions,
		ids:          ids,
		clock:        clock,
	}
}

// AbortRoomInput identifies the room to tear down and why.
type AbortRoomInput struct {
	RoomID string
	Reason string
}

// Execute refunds every unsettled stake in the room and closes it. It is
// idempotent in practice: an already-closed room is refused with
// game.ErrRoomClosed, and since the refund and the close share one
// transaction, a room can never be refunded twice.
func (uc *AbortRoomUseCase) Execute(ctx context.Context, in AbortRoomInput) (CloseRoomOutput, error) {
	if strings.TrimSpace(in.RoomID) == "" {
		return CloseRoomOutput{}, game.ErrRoomNotFound
	}
	reason := in.Reason
	if reason == "" {
		reason = ReasonShutdown
	}

	var (
		out     CloseRoomOutput
		summary ports.RoomSummary
	)
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		room, err := uc.rooms.GetForUpdate(ctx, in.RoomID)
		if err != nil {
			return err
		}
		if room.Status == game.RoomClosed {
			return game.ErrRoomClosed
		}

		rounds, err := uc.rounds.ListByRoom(ctx, room.ID)
		if err != nil {
			return err
		}
		now := uc.clock.Now()
		for _, round := range rounds {
			if round.Status != game.RoundBetting {
				continue
			}
			for _, bet := range round.Bets() {
				if bet.Settled() {
					continue
				}
				if err := uc.refund(ctx, bet.PlayerID, bet.Amount, round.ID, now); err != nil {
					return err
				}
			}
		}

		room.ForceClose(now)
		if err := uc.rooms.UpdateStatus(ctx, room.ID, room.Status, room.ClosedAt); err != nil {
			return err
		}

		members := room.Players()
		balances, err := memberBalances(ctx, uc.players, members)
		if err != nil {
			return err
		}
		summary = summaryOf(room)
		out = CloseRoomOutput{
			RoomID:   room.ID,
			Status:   room.Status,
			Closed:   true,
			Reason:   reason,
			Members:  members,
			Balances: balances,
		}
		return nil
	})
	if err != nil {
		return CloseRoomOutput{}, err
	}

	finalizeRoomCache(ctx, uc.state, uc.sessions, out.RoomID, summary, true, out.Members)
	return out, nil
}

// refund credits one open stake back to its player, with a ledger entry so the
// wallet still reconciles.
func (uc *AbortRoomUseCase) refund(ctx context.Context, playerID string, amount int64, roundID string, now time.Time) error {
	p, err := uc.players.GetForUpdate(ctx, playerID)
	if err != nil {
		return err
	}
	if err := p.Credit(amount); err != nil {
		return err
	}
	if err := uc.players.UpdateBalance(ctx, p.ID, p.Balance); err != nil {
		return err
	}
	id := roundID
	entry, err := player.NewTransaction(uc.ids.NewID(), p.ID, player.TransactionPayout, amount, &id, now)
	if err != nil {
		return err
	}
	return uc.transactions.Insert(ctx, entry)
}
