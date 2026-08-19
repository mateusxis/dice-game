// Package wallet holds the wallet use cases: deposit, withdraw and balance
// lookup. Every balance mutation runs inside a single TxManager transaction,
// locks the player row with GetForUpdate, and writes a ledger entry — that
// combination is what makes concurrent deposits/withdrawals/bets safe.
package wallet

import (
	"context"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// DepositInput is the amount (cents) to credit to a player's wallet.
type DepositInput struct {
	PlayerID string
	Amount   int64
}

// DepositOutput carries the balance after the deposit was applied.
type DepositOutput struct {
	Balance int64
}

// DepositUseCase credits a player's wallet. Deposits are allowed at any time,
// including while the player sits in an active room.
type DepositUseCase struct {
	tx           ports.TxManager
	players      ports.PlayerRepository
	transactions ports.TransactionRepository
	ids          ports.IDGenerator
	clock        ports.Clock
}

// NewDepositUseCase wires a DepositUseCase from its ports.
func NewDepositUseCase(tx ports.TxManager, players ports.PlayerRepository, transactions ports.TransactionRepository, ids ports.IDGenerator, clock ports.Clock) *DepositUseCase {
	return &DepositUseCase{tx: tx, players: players, transactions: transactions, ids: ids, clock: clock}
}

// Execute credits amount cents to the player's balance and appends a
// "deposit" ledger entry, all inside one transaction. It returns
// player.ErrInvalidAmount for a non-positive amount.
func (uc *DepositUseCase) Execute(ctx context.Context, in DepositInput) (DepositOutput, error) {
	if in.Amount <= 0 {
		return DepositOutput{}, player.ErrInvalidAmount
	}

	var out DepositOutput
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		p, err := uc.players.GetForUpdate(ctx, in.PlayerID)
		if err != nil {
			return err
		}
		if err := p.Credit(in.Amount); err != nil {
			return err
		}
		if err := uc.players.UpdateBalance(ctx, p.ID, p.Balance); err != nil {
			return err
		}

		entry, err := player.NewTransaction(uc.ids.NewID(), p.ID, player.TransactionDeposit, in.Amount, nil, uc.clock.Now())
		if err != nil {
			return err
		}
		if err := uc.transactions.Insert(ctx, entry); err != nil {
			return err
		}

		out = DepositOutput{Balance: p.Balance}
		return nil
	})
	if err != nil {
		return DepositOutput{}, err
	}
	return out, nil
}
