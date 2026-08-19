package game

import (
	"context"
	"strings"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// PlaceBetUseCase accepts a wager during a betting window and debits the stake
// immediately.
type PlaceBetUseCase struct {
	tx           ports.TxManager
	rooms        ports.RoomRepository
	rounds       ports.RoundRepository
	bets         ports.BetRepository
	players      ports.PlayerRepository
	transactions ports.TransactionRepository
	ids          ports.IDGenerator
	clock        ports.Clock
}

// NewPlaceBetUseCase wires a PlaceBetUseCase from its ports.
func NewPlaceBetUseCase(
	tx ports.TxManager,
	rooms ports.RoomRepository,
	rounds ports.RoundRepository,
	bets ports.BetRepository,
	players ports.PlayerRepository,
	transactions ports.TransactionRepository,
	ids ports.IDGenerator,
	clock ports.Clock,
) *PlaceBetUseCase {
	return &PlaceBetUseCase{
		tx:           tx,
		rooms:        rooms,
		rounds:       rounds,
		bets:         bets,
		players:      players,
		transactions: transactions,
		ids:          ids,
		clock:        clock,
	}
}

// Execute records a bet and debits the stake, atomically.
//
// Everything happens in one transaction:
//
//	room + live round are loaded and the aggregate validates membership, the
//	room state and the betting window (backend clock only);
//	the bet row is inserted first, so a duplicate submission is rejected by the
//	(round_id, player_id) unique constraint before any money moves;
//	the player row is then locked with GetForUpdate, debited, and a "bet"
//	ledger entry is appended.
//
// The output carries the accepted bet and nothing else: per the spec, placing
// a bet never returns a balance. The player learns their new balance from the
// balance.updated push at round end.
func (uc *PlaceBetUseCase) Execute(ctx context.Context, in PlaceBetInput) (PlaceBetOutput, error) {
	if strings.TrimSpace(in.RoomID) == "" {
		return PlaceBetOutput{}, game.ErrRoomNotFound
	}
	if strings.TrimSpace(in.PlayerID) == "" {
		return PlaceBetOutput{}, game.ErrInvalidID
	}
	if !in.Choice.Valid() {
		return PlaceBetOutput{}, game.ErrInvalidChoice
	}
	if in.Amount <= 0 {
		return PlaceBetOutput{}, game.ErrInvalidAmount
	}

	var out PlaceBetOutput
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		room, err := uc.rooms.GetByID(ctx, in.RoomID)
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
		bet, err := game.NewBet(uc.ids.NewID(), round.ID, in.PlayerID, in.Choice, in.Amount, now)
		if err != nil {
			return err
		}
		// Validates membership, room state, betting window and the in-memory
		// one-bet-per-round rule before anything is written.
		if err := room.PlaceBet(bet, now); err != nil {
			return err
		}
		if err := uc.bets.Insert(ctx, bet); err != nil {
			return err
		}

		p, err := uc.players.GetForUpdate(ctx, in.PlayerID)
		if err != nil {
			return err
		}
		if err := p.Debit(in.Amount); err != nil {
			return err
		}
		if err := uc.players.UpdateBalance(ctx, p.ID, p.Balance); err != nil {
			return err
		}
		roundID := round.ID
		entry, err := player.NewTransaction(uc.ids.NewID(), p.ID, player.TransactionBet, in.Amount, &roundID, now)
		if err != nil {
			return err
		}
		if err := uc.transactions.Insert(ctx, entry); err != nil {
			return err
		}

		out = PlaceBetOutput{
			BetID:       bet.ID,
			RoomID:      room.ID,
			RoundID:     round.ID,
			RoundNumber: round.Number,
			Choice:      bet.Choice,
			Amount:      bet.Amount,
		}
		return nil
	})
	if err != nil {
		return PlaceBetOutput{}, err
	}
	return out, nil
}
