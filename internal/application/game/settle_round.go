package game

import (
	"context"
	"strings"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// SettleRoundUseCase rolls the dice for a room's live round and pays the
// winners.
type SettleRoundUseCase struct {
	tx           ports.TxManager
	rooms        ports.RoomRepository
	rounds       ports.RoundRepository
	bets         ports.BetRepository
	players      ports.PlayerRepository
	transactions ports.TransactionRepository
	state        ports.RoomStateStore
	sessions     ports.PlayerSessionStore
	roller       game.DiceRoller
	ids          ports.IDGenerator
	clock        ports.Clock
}

// NewSettleRoundUseCase wires a SettleRoundUseCase from its ports.
func NewSettleRoundUseCase(
	tx ports.TxManager,
	rooms ports.RoomRepository,
	rounds ports.RoundRepository,
	bets ports.BetRepository,
	players ports.PlayerRepository,
	transactions ports.TransactionRepository,
	state ports.RoomStateStore,
	sessions ports.PlayerSessionStore,
	roller game.DiceRoller,
	ids ports.IDGenerator,
	clock ports.Clock,
) *SettleRoundUseCase {
	return &SettleRoundUseCase{
		tx:           tx,
		rooms:        rooms,
		rounds:       rounds,
		bets:         bets,
		players:      players,
		transactions: transactions,
		state:        state,
		sessions:     sessions,
		roller:       roller,
		ids:          ids,
		clock:        clock,
	}
}

// Execute settles the live round of a room.
//
// One transaction covers the whole settlement, so a crash can never pay some
// winners and not others:
//
//	the dice are rolled through the DiceRoller (crypto/rand in production);
//	the aggregate resolves every bet and, if this was round ten or a close was
//	pending, marks the room closed in the same step;
//	the round row and each bet row record their result;
//	each winner's row is locked, credited Payout(stake) = stake*192/100 and
//	given a "payout" ledger entry keyed to the round;
//	finally every member's balance is read back so the caller can push
//	balance.updated — the one moment the spec allows a balance to reach a
//	client during play.
//
// Settling twice is not possible: the round repository's UPDATE is guarded on
// status = 'betting' and the aggregate refuses an already-settled round.
func (uc *SettleRoundUseCase) Execute(ctx context.Context, in SettleRoundInput) (SettleRoundOutput, error) {
	if strings.TrimSpace(in.RoomID) == "" {
		return SettleRoundOutput{}, game.ErrRoomNotFound
	}

	var (
		out     SettleRoundOutput
		summary ports.RoomSummary
	)
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		room, err := uc.rooms.GetForUpdate(ctx, in.RoomID)
		if err != nil {
			return err
		}
		round, err := attachLiveRound(ctx, uc.rounds, room)
		if err != nil {
			return err
		}
		if round == nil {
			return game.ErrNoActiveRound
		}

		now := uc.clock.Now()
		results, err := room.SettleRound(uc.roller, now)
		if err != nil {
			return err
		}
		if err := uc.rounds.Settle(ctx, round); err != nil {
			return err
		}
		for _, res := range results {
			if err := uc.bets.SettleBet(ctx, res.BetID, res.Won, res.Payout); err != nil {
				return err
			}
			if !res.Won || res.Payout <= 0 {
				continue
			}
			if err := uc.credit(ctx, res.PlayerID, res.Payout, round.ID, now); err != nil {
				return err
			}
		}

		if room.Status == game.RoomClosed {
			if err := uc.rooms.UpdateStatus(ctx, room.ID, room.Status, room.ClosedAt); err != nil {
				return err
			}
		}

		members := room.Players()
		balances, err := memberBalances(ctx, uc.players, members)
		if err != nil {
			return err
		}

		summary = summaryOf(room)
		out = SettleRoundOutput{
			RoomID:      room.ID,
			RoundID:     round.ID,
			RoundNumber: round.Number,
			Die1:        round.Die1,
			Die2:        round.Die2,
			Sum:         round.Die1 + round.Die2,
			Outcome:     round.Outcome,
			Results:     results,
			Members:     members,
			Balances:    balances,
			RoomClosed:  room.Status == game.RoomClosed,
		}
		if out.RoomClosed {
			out.CloseReason = ReasonMaxRounds
			if round.Number < room.MaxRounds {
				// The cap was not reached, so the room was in "closing": the
				// owner's request has been honoured now that the round ended.
				out.CloseReason = ReasonOwnerClosed
			}
		}
		return nil
	})
	if err != nil {
		return SettleRoundOutput{}, err
	}

	finalizeRoomCache(ctx, uc.state, uc.sessions, out.RoomID, summary, out.RoomClosed, out.Members)
	return out, nil
}

// credit pays one winner inside the settlement transaction: lock the row, add
// the gross payout, persist the balance, append the ledger entry.
func (uc *SettleRoundUseCase) credit(ctx context.Context, playerID string, payout int64, roundID string, now time.Time) error {
	p, err := uc.players.GetForUpdate(ctx, playerID)
	if err != nil {
		return err
	}
	if err := p.Credit(payout); err != nil {
		return err
	}
	if err := uc.players.UpdateBalance(ctx, p.ID, p.Balance); err != nil {
		return err
	}
	id := roundID
	entry, err := player.NewTransaction(uc.ids.NewID(), p.ID, player.TransactionPayout, payout, &id, now)
	if err != nil {
		return err
	}
	return uc.transactions.Insert(ctx, entry)
}
