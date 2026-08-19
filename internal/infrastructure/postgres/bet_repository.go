package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/infrastructure/postgres/sqlcgen"
)

// BetRepository is the PostgreSQL adapter for ports.BetRepository.
type BetRepository struct {
	pool *pgxpool.Pool
}

var _ ports.BetRepository = (*BetRepository)(nil)

// NewBetRepository builds a bet repository over a pool.
func NewBetRepository(pool *pgxpool.Pool) *BetRepository {
	return &BetRepository{pool: pool}
}

// Insert stores a bet. A bets_round_player_key violation means the player
// already bet in this round — the constraint, not the application, is what
// wins the race between two concurrent submissions.
func (r *BetRepository) Insert(ctx context.Context, b *game.Bet) error {
	id, err := toUUID(b.ID)
	if err != nil {
		return fmt.Errorf("bet id: %w", err)
	}
	roundID, err := toUUID(b.RoundID)
	if err != nil {
		return fmt.Errorf("bet round id: %w", err)
	}
	playerID, err := toUUID(b.PlayerID)
	if err != nil {
		return fmt.Errorf("bet player id: %w", err)
	}
	if _, err := querier(ctx, r.pool).InsertBet(ctx, sqlcgen.InsertBetParams{
		ID:       id,
		RoundID:  roundID,
		PlayerID: playerID,
		Choice:   string(b.Choice),
		Amount:   b.Amount,
		PlacedAt: toTimestamptz(b.PlacedAt),
	}); err != nil {
		switch pgErrorCode(err) {
		case codeUniqueViolation:
			return game.ErrDuplicateBet
		case codeCheckViolation:
			return game.ErrInvalidAmount
		case codeForeignKeyViolation:
			return game.ErrRoundNotFound
		}
		return fmt.Errorf("insert bet: %w", err)
	}
	return nil
}

// GetByRoundAndPlayer returns the single bet a player placed in a round.
func (r *BetRepository) GetByRoundAndPlayer(ctx context.Context, roundID, playerID string) (*game.Bet, error) {
	rid, err := toUUID(roundID)
	if err != nil {
		return nil, game.ErrRoundNotFound
	}
	pid, err := toUUID(playerID)
	if err != nil {
		return nil, game.ErrInvalidID
	}
	row, err := querier(ctx, r.pool).GetBetByRoundAndPlayer(ctx, sqlcgen.GetBetByRoundAndPlayerParams{
		RoundID:  rid,
		PlayerID: pid,
	})
	if err != nil {
		if isNoRows(err) {
			return nil, game.ErrBetNotFound
		}
		return nil, fmt.Errorf("get bet: %w", err)
	}
	return toDomainBet(row), nil
}

// ListByRound returns every bet of a round in placement order.
func (r *BetRepository) ListByRound(ctx context.Context, roundID string) ([]*game.Bet, error) {
	rid, err := toUUID(roundID)
	if err != nil {
		return nil, game.ErrRoundNotFound
	}
	rows, err := querier(ctx, r.pool).ListBetsByRound(ctx, rid)
	if err != nil {
		return nil, fmt.Errorf("list bets: %w", err)
	}
	out := make([]*game.Bet, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainBet(row))
	}
	return out, nil
}

// SettleBet records the result of a bet. The WHERE won IS NULL guard keeps
// settlement idempotent.
func (r *BetRepository) SettleBet(ctx context.Context, betID string, won bool, payout int64) error {
	id, err := toUUID(betID)
	if err != nil {
		return game.ErrBetNotFound
	}
	settledAt := timeNow()
	if _, err := querier(ctx, r.pool).SettleBet(ctx, sqlcgen.SettleBetParams{
		ID:        id,
		Won:       toBool(won),
		Payout:    toInt8(payout),
		SettledAt: toTimestamptz(settledAt),
	}); err != nil {
		if isNoRows(err) {
			return game.ErrRoundAlreadySettled
		}
		return fmt.Errorf("settle bet: %w", err)
	}
	return nil
}

// timeNow is a package-level indirection so tests can freeze settlement
// timestamps without threading a clock through every repository signature.
var timeNow = func() time.Time { return time.Now().UTC() }

func toDomainBet(row sqlcgen.Bet) *game.Bet {
	return &game.Bet{
		ID:       fromUUID(row.ID),
		RoundID:  fromUUID(row.RoundID),
		PlayerID: fromUUID(row.PlayerID),
		Choice:   game.Choice(row.Choice),
		Amount:   row.Amount,
		Won:      fromNullBool(row.Won),
		Payout:   fromNullInt8(row.Payout),
		PlacedAt: fromTimestamptz(row.PlacedAt),
	}
}
