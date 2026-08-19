package wallet

import (
	"context"

	"github.com/mateusxis/cassino/internal/application/ports"
)

// BalanceOutput carries the player's current balance.
type BalanceOutput struct {
	Balance int64
}

// GetBalanceUseCase reads a player's current wallet balance.
type GetBalanceUseCase struct {
	players ports.PlayerRepository
}

// NewGetBalanceUseCase wires a GetBalanceUseCase from its ports.
func NewGetBalanceUseCase(players ports.PlayerRepository) *GetBalanceUseCase {
	return &GetBalanceUseCase{players: players}
}

// Execute returns the player's balance, or player.ErrNotFound.
func (uc *GetBalanceUseCase) Execute(ctx context.Context, playerID string) (BalanceOutput, error) {
	p, err := uc.players.GetByID(ctx, playerID)
	if err != nil {
		return BalanceOutput{}, err
	}
	return BalanceOutput{Balance: p.Balance}, nil
}
