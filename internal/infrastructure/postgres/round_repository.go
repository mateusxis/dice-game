package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/infrastructure/postgres/sqlcgen"
)

// RoundRepository is the PostgreSQL adapter for ports.RoundRepository. Rounds
// are always loaded together with their bets so the aggregate can enforce the
// one-bet-per-player rule in memory as well as in the database.
type RoundRepository struct {
	pool *pgxpool.Pool
	bets *BetRepository
}

var _ ports.RoundRepository = (*RoundRepository)(nil)

// NewRoundRepository builds a round repository over a pool.
func NewRoundRepository(pool *pgxpool.Pool, bets *BetRepository) *RoundRepository {
	return &RoundRepository{pool: pool, bets: bets}
}

// Create opens a round row in the betting state.
func (r *RoundRepository) Create(ctx context.Context, round *game.Round) error {
	id, err := toUUID(round.ID)
	if err != nil {
		return fmt.Errorf("round id: %w", err)
	}
	roomID, err := toUUID(round.RoomID)
	if err != nil {
		return fmt.Errorf("round room id: %w", err)
	}
	if _, err := querier(ctx, r.pool).CreateRound(ctx, sqlcgen.CreateRoundParams{
		ID:              id,
		RoomID:          roomID,
		Number:          int32(round.Number),
		BettingOpensAt:  toTimestamptz(round.BettingOpensAt),
		BettingClosesAt: toTimestamptz(round.BettingClosesAt),
	}); err != nil {
		if pgErrorCode(err) == codeUniqueViolation {
			return game.ErrRoundInProgress
		}
		return fmt.Errorf("create round: %w", err)
	}
	return nil
}

// GetByID loads a round with its bets.
func (r *RoundRepository) GetByID(ctx context.Context, id string) (*game.Round, error) {
	rid, err := toUUID(id)
	if err != nil {
		return nil, game.ErrRoundNotFound
	}
	row, err := querier(ctx, r.pool).GetRoundByID(ctx, rid)
	if err != nil {
		if isNoRows(err) {
			return nil, game.ErrRoundNotFound
		}
		return nil, fmt.Errorf("get round: %w", err)
	}
	return r.hydrate(ctx, row)
}

// GetByRoomAndNumber loads one numbered round of a room.
func (r *RoundRepository) GetByRoomAndNumber(ctx context.Context, roomID string, number int) (*game.Round, error) {
	rid, err := toUUID(roomID)
	if err != nil {
		return nil, game.ErrRoomNotFound
	}
	row, err := querier(ctx, r.pool).GetRoundByRoomAndNumber(ctx, sqlcgen.GetRoundByRoomAndNumberParams{
		RoomID: rid,
		Number: int32(number),
	})
	if err != nil {
		if isNoRows(err) {
			return nil, game.ErrRoundNotFound
		}
		return nil, fmt.Errorf("get round by number: %w", err)
	}
	return r.hydrate(ctx, row)
}

// GetCurrent loads the highest-numbered round of a room.
func (r *RoundRepository) GetCurrent(ctx context.Context, roomID string) (*game.Round, error) {
	rid, err := toUUID(roomID)
	if err != nil {
		return nil, game.ErrRoomNotFound
	}
	row, err := querier(ctx, r.pool).GetCurrentRound(ctx, rid)
	if err != nil {
		if isNoRows(err) {
			return nil, game.ErrRoundNotFound
		}
		return nil, fmt.Errorf("get current round: %w", err)
	}
	return r.hydrate(ctx, row)
}

// ListByRoom loads every round of a room in play order.
func (r *RoundRepository) ListByRoom(ctx context.Context, roomID string) ([]*game.Round, error) {
	rid, err := toUUID(roomID)
	if err != nil {
		return nil, game.ErrRoomNotFound
	}
	rows, err := querier(ctx, r.pool).ListRoundsByRoom(ctx, rid)
	if err != nil {
		return nil, fmt.Errorf("list rounds: %w", err)
	}
	out := make([]*game.Round, 0, len(rows))
	for _, row := range rows {
		round, err := r.hydrate(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, round)
	}
	return out, nil
}

// Settle persists the dice, outcome and settled status. The WHERE clause on
// status = 'betting' makes the update idempotent under concurrent settlement
// attempts: a second caller gets ErrRoundAlreadySettled instead of overwriting
// the recorded dice.
func (r *RoundRepository) Settle(ctx context.Context, round *game.Round) error {
	id, err := toUUID(round.ID)
	if err != nil {
		return game.ErrRoundNotFound
	}
	settledAt := round.SettledAt
	if settledAt == nil {
		return game.ErrRoundAlreadySettled
	}
	_, err = querier(ctx, r.pool).SettleRound(ctx, sqlcgen.SettleRoundParams{
		ID:        id,
		Die1:      toInt2(round.Die1),
		Die2:      toInt2(round.Die2),
		Outcome:   toText(string(round.Outcome)),
		SettledAt: toNullTimestamptz(settledAt),
	})
	if err != nil {
		if isNoRows(err) {
			return game.ErrRoundAlreadySettled
		}
		return fmt.Errorf("settle round: %w", err)
	}
	return nil
}

func (r *RoundRepository) hydrate(ctx context.Context, row sqlcgen.Round) (*game.Round, error) {
	var bets []*game.Bet
	if r.bets != nil {
		var err error
		bets, err = r.bets.ListByRound(ctx, fromUUID(row.ID))
		if err != nil {
			return nil, err
		}
	}
	outcome := game.Outcome("")
	if row.Outcome.Valid {
		outcome = game.Outcome(row.Outcome.String)
	}
	return game.RehydrateRound(
		fromUUID(row.ID),
		fromUUID(row.RoomID),
		int(row.Number),
		fromTimestamptz(row.BettingOpensAt),
		fromTimestamptz(row.BettingClosesAt),
		fromInt2(row.Die1),
		fromInt2(row.Die2),
		outcome,
		game.RoundStatus(row.Status),
		fromNullTimestamptz(row.SettledAt),
		bets,
	)
}
