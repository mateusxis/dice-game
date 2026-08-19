package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/player"
	"github.com/mateusxis/cassino/internal/infrastructure/postgres/sqlcgen"
)

// PlayerRepository is the PostgreSQL adapter for ports.PlayerRepository.
type PlayerRepository struct {
	pool *pgxpool.Pool
}

var _ ports.PlayerRepository = (*PlayerRepository)(nil)

// NewPlayerRepository builds a player repository over a pool.
func NewPlayerRepository(pool *pgxpool.Pool) *PlayerRepository {
	return &PlayerRepository{pool: pool}
}

// Create inserts a new account, mapping the e-mail unique index to
// player.ErrEmailAlreadyUsed.
func (r *PlayerRepository) Create(ctx context.Context, p *player.Player) error {
	id, err := toUUID(p.ID)
	if err != nil {
		return fmt.Errorf("player id: %w", err)
	}
	_, err = querier(ctx, r.pool).CreatePlayer(ctx, sqlcgen.CreatePlayerParams{
		ID:           id,
		Email:        p.Email,
		PasswordHash: p.PasswordHash,
		Balance:      p.Balance,
		CreatedAt:    toTimestamptz(p.CreatedAt),
	})
	if err != nil {
		if pgErrorCode(err) == codeUniqueViolation {
			return player.ErrEmailAlreadyUsed
		}
		return fmt.Errorf("create player: %w", err)
	}
	return nil
}

// GetByID loads a player by primary key.
func (r *PlayerRepository) GetByID(ctx context.Context, id string) (*player.Player, error) {
	pid, err := toUUID(id)
	if err != nil {
		return nil, player.ErrNotFound
	}
	row, err := querier(ctx, r.pool).GetPlayerByID(ctx, pid)
	if err != nil {
		if isNoRows(err) {
			return nil, player.ErrNotFound
		}
		return nil, fmt.Errorf("get player: %w", err)
	}
	return toDomainPlayer(row)
}

// GetByEmail loads a player by (case-insensitive) e-mail.
func (r *PlayerRepository) GetByEmail(ctx context.Context, email string) (*player.Player, error) {
	row, err := querier(ctx, r.pool).GetPlayerByEmail(ctx, email)
	if err != nil {
		if isNoRows(err) {
			return nil, player.ErrNotFound
		}
		return nil, fmt.Errorf("get player by email: %w", err)
	}
	return toDomainPlayer(row)
}

// GetForUpdate loads a player with the row locked. Callers must already be
// inside a TxManager transaction, otherwise the lock is released immediately
// and offers no protection.
func (r *PlayerRepository) GetForUpdate(ctx context.Context, id string) (*player.Player, error) {
	pid, err := toUUID(id)
	if err != nil {
		return nil, player.ErrNotFound
	}
	row, err := querier(ctx, r.pool).GetPlayerForUpdate(ctx, pid)
	if err != nil {
		if isNoRows(err) {
			return nil, player.ErrNotFound
		}
		return nil, fmt.Errorf("lock player: %w", err)
	}
	return toDomainPlayer(row)
}

// UpdateBalance writes an absolute balance. The players_balance_non_negative
// CHECK constraint is the last line of defence against a negative result.
func (r *PlayerRepository) UpdateBalance(ctx context.Context, id string, balance int64) error {
	pid, err := toUUID(id)
	if err != nil {
		return player.ErrNotFound
	}
	_, err = querier(ctx, r.pool).UpdatePlayerBalance(ctx, sqlcgen.UpdatePlayerBalanceParams{
		ID:      pid,
		Balance: balance,
	})
	if err != nil {
		if isNoRows(err) {
			return player.ErrNotFound
		}
		if pgErrorCode(err) == codeCheckViolation {
			return player.ErrInsufficientBalance
		}
		return fmt.Errorf("update balance: %w", err)
	}
	return nil
}

func toDomainPlayer(row sqlcgen.Player) (*player.Player, error) {
	return player.Rehydrate(
		fromUUID(row.ID),
		row.Email,
		row.PasswordHash,
		row.Balance,
		fromTimestamptz(row.CreatedAt),
	)
}

// TransactionRepository is the PostgreSQL adapter for the wallet ledger.
type TransactionRepository struct {
	pool *pgxpool.Pool
}

var _ ports.TransactionRepository = (*TransactionRepository)(nil)

// NewTransactionRepository builds a ledger repository over a pool.
func NewTransactionRepository(pool *pgxpool.Pool) *TransactionRepository {
	return &TransactionRepository{pool: pool}
}

// Insert appends a ledger entry.
func (r *TransactionRepository) Insert(ctx context.Context, t *player.Transaction) error {
	id, err := toUUID(t.ID)
	if err != nil {
		return fmt.Errorf("transaction id: %w", err)
	}
	playerID, err := toUUID(t.PlayerID)
	if err != nil {
		return fmt.Errorf("transaction player id: %w", err)
	}
	roundID, err := toNullUUID(t.RoundID)
	if err != nil {
		return fmt.Errorf("transaction round id: %w", err)
	}
	_, err = querier(ctx, r.pool).InsertTransaction(ctx, sqlcgen.InsertTransactionParams{
		ID:        id,
		PlayerID:  playerID,
		Type:      string(t.Type),
		Amount:    t.Amount,
		RoundID:   roundID,
		CreatedAt: toTimestamptz(t.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("insert transaction: %w", err)
	}
	return nil
}

// ListByPlayer returns a page of ledger entries, newest first.
func (r *TransactionRepository) ListByPlayer(ctx context.Context, playerID string, limit, offset int32) ([]*player.Transaction, error) {
	pid, err := toUUID(playerID)
	if err != nil {
		return nil, player.ErrNotFound
	}
	rows, err := querier(ctx, r.pool).ListTransactionsByPlayer(ctx, sqlcgen.ListTransactionsByPlayerParams{
		PlayerID: pid,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	out := make([]*player.Transaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, &player.Transaction{
			ID:        fromUUID(row.ID),
			PlayerID:  fromUUID(row.PlayerID),
			Type:      player.TransactionType(row.Type),
			Amount:    row.Amount,
			RoundID:   fromNullUUID(row.RoundID),
			CreatedAt: fromTimestamptz(row.CreatedAt),
		})
	}
	return out, nil
}
