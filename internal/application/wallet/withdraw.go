package wallet

import (
	"context"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// WithdrawInput is the amount (cents) to debit from a player's wallet.
type WithdrawInput struct {
	PlayerID string
	Amount   int64
}

// WithdrawOutput carries the balance after the withdrawal was applied.
type WithdrawOutput struct {
	Balance int64
}

// WithdrawUseCase debits a player's wallet. Withdrawals are blocked while the
// player sits in an active room and are capped at the current balance.
type WithdrawUseCase struct {
	tx           ports.TxManager
	players      ports.PlayerRepository
	transactions ports.TransactionRepository
	rooms        ports.RoomRepository
	sessions     ports.PlayerSessionStore
	ids          ports.IDGenerator
	clock        ports.Clock
}

// NewWithdrawUseCase wires a WithdrawUseCase from its ports.
func NewWithdrawUseCase(
	tx ports.TxManager,
	players ports.PlayerRepository,
	transactions ports.TransactionRepository,
	rooms ports.RoomRepository,
	sessions ports.PlayerSessionStore,
	ids ports.IDGenerator,
	clock ports.Clock,
) *WithdrawUseCase {
	return &WithdrawUseCase{
		tx:           tx,
		players:      players,
		transactions: transactions,
		rooms:        rooms,
		sessions:     sessions,
		ids:          ids,
		clock:        clock,
	}
}

// Execute debits amount cents from the player's balance and appends a
// "withdraw" ledger entry, all inside one transaction. It returns
// player.ErrInvalidAmount for a non-positive amount, player.ErrWithdrawalBlocked
// while the player is seated in an active room, and player.ErrInsufficientBalance
// when the balance cannot cover it.
//
// The active-room check runs twice by design: a cheap Redis read before
// opening the transaction (so a blocked withdrawal never pays for a
// round-trip and a row lock), and the authoritative PostgreSQL check again
// inside the transaction, after the player row is locked, which closes the
// race window between "player joins a room" and "withdrawal commits".
func (uc *WithdrawUseCase) Execute(ctx context.Context, in WithdrawInput) (WithdrawOutput, error) {
	if in.Amount <= 0 {
		return WithdrawOutput{}, player.ErrInvalidAmount
	}

	if uc.sessions != nil {
		roomID, err := uc.sessions.ActiveRoom(ctx, in.PlayerID)
		if err != nil {
			return WithdrawOutput{}, err
		}
		if roomID != "" {
			return WithdrawOutput{}, player.ErrWithdrawalBlocked
		}
	}

	var out WithdrawOutput
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		if uc.rooms != nil {
			roomID, err := uc.rooms.FindActiveRoomForPlayer(ctx, in.PlayerID)
			if err != nil {
				return err
			}
			if roomID != "" {
				return player.ErrWithdrawalBlocked
			}
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

		entry, err := player.NewTransaction(uc.ids.NewID(), p.ID, player.TransactionWithdraw, in.Amount, nil, uc.clock.Now())
		if err != nil {
			return err
		}
		if err := uc.transactions.Insert(ctx, entry); err != nil {
			return err
		}

		out = WithdrawOutput{Balance: p.Balance}
		return nil
	})
	if err != nil {
		return WithdrawOutput{}, err
	}
	return out, nil
}
