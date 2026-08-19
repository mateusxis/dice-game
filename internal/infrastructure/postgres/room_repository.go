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

// RoomRepository is the PostgreSQL adapter for ports.RoomRepository.
type RoomRepository struct {
	pool *pgxpool.Pool
}

var (
	_ ports.RoomRepository         = (*RoomRepository)(nil)
	_ ports.RoomRecoveryRepository = (*RoomRepository)(nil)
)

// NewRoomRepository builds a room repository over a pool.
func NewRoomRepository(pool *pgxpool.Pool) *RoomRepository {
	return &RoomRepository{pool: pool}
}

// Create inserts the room and seats its owner in one transaction-friendly
// pair of statements. Callers normally wrap it in TxManager.WithinTx.
func (r *RoomRepository) Create(ctx context.Context, room *game.Room) error {
	q := querier(ctx, r.pool)
	id, err := toUUID(room.ID)
	if err != nil {
		return fmt.Errorf("room id: %w", err)
	}
	ownerID, err := toUUID(room.OwnerID)
	if err != nil {
		return fmt.Errorf("room owner id: %w", err)
	}
	if _, err := q.CreateRoom(ctx, sqlcgen.CreateRoomParams{
		ID:           id,
		OwnerID:      ownerID,
		Status:       string(room.Status),
		MaxRounds:    int32(room.MaxRounds),
		MaxPlayers:   int32(room.MaxPlayers),
		CurrentRound: int32(room.CurrentRound),
		CreatedAt:    toTimestamptz(room.CreatedAt),
	}); err != nil {
		return fmt.Errorf("create room: %w", err)
	}
	for _, playerID := range room.Players() {
		if err := r.AddPlayer(ctx, room.ID, playerID, room.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

// GetByID loads a room aggregate with its seat set.
func (r *RoomRepository) GetByID(ctx context.Context, id string) (*game.Room, error) {
	return r.load(ctx, id, false)
}

// GetForUpdate loads a room with the row locked for a lifecycle change.
func (r *RoomRepository) GetForUpdate(ctx context.Context, id string) (*game.Room, error) {
	return r.load(ctx, id, true)
}

func (r *RoomRepository) load(ctx context.Context, id string, forUpdate bool) (*game.Room, error) {
	q := querier(ctx, r.pool)
	rid, err := toUUID(id)
	if err != nil {
		return nil, game.ErrRoomNotFound
	}
	var row sqlcgen.Room
	if forUpdate {
		row, err = q.GetRoomForUpdate(ctx, rid)
	} else {
		row, err = q.GetRoomByID(ctx, rid)
	}
	if err != nil {
		if isNoRows(err) {
			return nil, game.ErrRoomNotFound
		}
		return nil, fmt.Errorf("get room: %w", err)
	}
	seatRows, err := q.ListRoomPlayers(ctx, rid)
	if err != nil {
		return nil, fmt.Errorf("list room players: %w", err)
	}
	seats := make(map[string]time.Time, len(seatRows))
	for _, s := range seatRows {
		seats[fromUUID(s.PlayerID)] = fromTimestamptz(s.JoinedAt)
	}
	// The live round is loaded on demand by the round repository; the room
	// aggregate is rehydrated without it and the game engine attaches it.
	return game.RehydrateRoom(
		fromUUID(row.ID),
		fromUUID(row.OwnerID),
		game.RoomStatus(row.Status),
		int(row.CurrentRound),
		fromTimestamptz(row.CreatedAt),
		fromNullTimestamptz(row.ClosedAt),
		seats,
		nil,
	)
}

// ListOpen returns a page of rooms currently accepting players.
func (r *RoomRepository) ListOpen(ctx context.Context, limit, offset int32) ([]ports.RoomSummary, error) {
	rows, err := querier(ctx, r.pool).ListOpenRooms(ctx, sqlcgen.ListOpenRoomsParams{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list open rooms: %w", err)
	}
	out := make([]ports.RoomSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, ports.RoomSummary{
			ID:           fromUUID(row.ID),
			OwnerID:      fromUUID(row.OwnerID),
			Status:       game.RoomStatus(row.Status),
			CurrentRound: int(row.CurrentRound),
			MaxRounds:    int(row.MaxRounds),
			PlayerCount:  int(row.PlayerCount),
			MaxPlayers:   int(row.MaxPlayers),
			CreatedAt:    fromTimestamptz(row.CreatedAt),
		})
	}
	return out, nil
}

// ListActiveRoomIDs returns every non-closed room, oldest first. Only the
// startup recovery pass uses it: rooms still marked open or closing after a
// restart belong to a process that is gone.
func (r *RoomRepository) ListActiveRoomIDs(ctx context.Context) ([]string, error) {
	rows, err := querier(ctx, r.pool).ListActiveRoomIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active rooms: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		out = append(out, fromUUID(id))
	}
	return out, nil
}

// UpdateStatus persists a lifecycle transition together with closed_at.
func (r *RoomRepository) UpdateStatus(ctx context.Context, id string, status game.RoomStatus, closedAt *time.Time) error {
	rid, err := toUUID(id)
	if err != nil {
		return game.ErrRoomNotFound
	}
	_, err = querier(ctx, r.pool).UpdateRoomStatus(ctx, sqlcgen.UpdateRoomStatusParams{
		ID:       rid,
		Status:   string(status),
		ClosedAt: toNullTimestamptz(closedAt),
	})
	if err != nil {
		if isNoRows(err) {
			return game.ErrRoomNotFound
		}
		return fmt.Errorf("update room status: %w", err)
	}
	return nil
}

// SetCurrentRound persists the round counter.
func (r *RoomRepository) SetCurrentRound(ctx context.Context, id string, currentRound int) error {
	rid, err := toUUID(id)
	if err != nil {
		return game.ErrRoomNotFound
	}
	_, err = querier(ctx, r.pool).UpdateRoomCurrentRound(ctx, sqlcgen.UpdateRoomCurrentRoundParams{
		ID:           rid,
		CurrentRound: int32(currentRound),
	})
	if err != nil {
		if isNoRows(err) {
			return game.ErrRoomNotFound
		}
		return fmt.Errorf("update current round: %w", err)
	}
	return nil
}

// AddPlayer seats a player, mapping the (room_id, player_id) primary key
// violation to game.ErrAlreadyJoined.
func (r *RoomRepository) AddPlayer(ctx context.Context, roomID, playerID string, joinedAt time.Time) error {
	rid, err := toUUID(roomID)
	if err != nil {
		return game.ErrRoomNotFound
	}
	pid, err := toUUID(playerID)
	if err != nil {
		return game.ErrInvalidID
	}
	if _, err := querier(ctx, r.pool).AddRoomPlayer(ctx, sqlcgen.AddRoomPlayerParams{
		RoomID:   rid,
		PlayerID: pid,
		JoinedAt: toTimestamptz(joinedAt),
	}); err != nil {
		switch pgErrorCode(err) {
		case codeUniqueViolation:
			return game.ErrAlreadyJoined
		case codeForeignKeyViolation:
			return game.ErrRoomNotFound
		}
		return fmt.Errorf("add room player: %w", err)
	}
	return nil
}

// RemovePlayer unseats a player.
func (r *RoomRepository) RemovePlayer(ctx context.Context, roomID, playerID string) error {
	rid, err := toUUID(roomID)
	if err != nil {
		return game.ErrRoomNotFound
	}
	pid, err := toUUID(playerID)
	if err != nil {
		return game.ErrInvalidID
	}
	affected, err := querier(ctx, r.pool).RemoveRoomPlayer(ctx, sqlcgen.RemoveRoomPlayerParams{
		RoomID:   rid,
		PlayerID: pid,
	})
	if err != nil {
		return fmt.Errorf("remove room player: %w", err)
	}
	if affected == 0 {
		return game.ErrNotAMember
	}
	return nil
}

// CountPlayers returns the seat count.
func (r *RoomRepository) CountPlayers(ctx context.Context, roomID string) (int, error) {
	rid, err := toUUID(roomID)
	if err != nil {
		return 0, game.ErrRoomNotFound
	}
	n, err := querier(ctx, r.pool).CountRoomPlayers(ctx, rid)
	if err != nil {
		return 0, fmt.Errorf("count room players: %w", err)
	}
	return int(n), nil
}

// FindActiveRoomForPlayer returns the non-closed room the player sits in, or
// "" when they are free to join one.
func (r *RoomRepository) FindActiveRoomForPlayer(ctx context.Context, playerID string) (string, error) {
	pid, err := toUUID(playerID)
	if err != nil {
		return "", game.ErrInvalidID
	}
	id, err := querier(ctx, r.pool).FindActiveRoomForPlayer(ctx, pid)
	if err != nil {
		if isNoRows(err) {
			return "", nil
		}
		return "", fmt.Errorf("find active room: %w", err)
	}
	return fromUUID(id), nil
}
