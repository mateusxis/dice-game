package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// RoomStateStore caches open-room summaries and hands out the short-lived
// mutex that serializes joins into a single room.
type RoomStateStore struct {
	client *redis.Client
}

var _ ports.RoomStateStore = (*RoomStateStore)(nil)

// NewRoomStateStore builds the room cache over a Redis client.
func NewRoomStateStore(client *redis.Client) *RoomStateStore {
	return &RoomStateStore{client: client}
}

// cachedRoom is the wire shape stored in Redis. It is deliberately separate
// from ports.RoomSummary so a field rename in the port cannot silently
// invalidate everything already cached.
type cachedRoom struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"owner_id"`
	Status       string    `json:"status"`
	CurrentRound int       `json:"current_round"`
	MaxRounds    int       `json:"max_rounds"`
	PlayerCount  int       `json:"player_count"`
	MaxPlayers   int       `json:"max_players"`
	CreatedAt    time.Time `json:"created_at"`
}

// SaveRoom upserts a room summary. An open room is also indexed in the
// open-rooms hash; any other status is evicted from it, so the listing never
// advertises a closing or closed table.
func (s *RoomStateStore) SaveRoom(ctx context.Context, summary ports.RoomSummary, ttl time.Duration) error {
	payload, err := json.Marshal(toCached(summary))
	if err != nil {
		return fmt.Errorf("redis: marshal room %s: %w", summary.ID, err)
	}
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, keyRoomPrefix+summary.ID, payload, ttl)
	if summary.Status == game.RoomOpen {
		pipe.HSet(ctx, keyOpenRooms, summary.ID, payload)
	} else {
		pipe.HDel(ctx, keyOpenRooms, summary.ID)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis: save room %s: %w", summary.ID, err)
	}
	return nil
}

// GetRoom returns a cached summary; found is false on a cache miss, which the
// caller resolves by reading PostgreSQL.
func (s *RoomStateStore) GetRoom(ctx context.Context, roomID string) (ports.RoomSummary, bool, error) {
	raw, err := s.client.Get(ctx, keyRoomPrefix+roomID).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ports.RoomSummary{}, false, nil
		}
		return ports.RoomSummary{}, false, fmt.Errorf("redis: get room %s: %w", roomID, err)
	}
	var c cachedRoom
	if err := json.Unmarshal(raw, &c); err != nil {
		// A corrupt entry is treated as a miss rather than an outage.
		return ports.RoomSummary{}, false, nil
	}
	return fromCached(c), true, nil
}

// ListOpenRooms returns every cached open room.
func (s *RoomStateStore) ListOpenRooms(ctx context.Context) ([]ports.RoomSummary, error) {
	entries, err := s.client.HGetAll(ctx, keyOpenRooms).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: list open rooms: %w", err)
	}
	out := make([]ports.RoomSummary, 0, len(entries))
	for _, raw := range entries {
		var c cachedRoom
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			continue
		}
		out = append(out, fromCached(c))
	}
	return out, nil
}

// RemoveRoom evicts a room from both the per-room key and the open index.
func (s *RoomStateStore) RemoveRoom(ctx context.Context, roomID string) error {
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, keyRoomPrefix+roomID)
	pipe.HDel(ctx, keyOpenRooms, roomID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis: remove room %s: %w", roomID, err)
	}
	return nil
}

// AcquireJoinLock takes a SET NX mutex around one room's seat count so two
// simultaneous joins cannot both observe five occupied seats and push the room
// to seven. The returned release function deletes the key only if it still
// holds our token, so a lock that already expired is never stolen back.
func (s *RoomStateStore) AcquireJoinLock(ctx context.Context, roomID string, ttl time.Duration) (func(context.Context) error, bool, error) {
	key := keyJoinLock + roomID
	token := uuid.NewString()
	ok, err := s.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, false, fmt.Errorf("redis: acquire join lock %s: %w", roomID, err)
	}
	if !ok {
		return nil, false, nil
	}
	release := func(releaseCtx context.Context) error {
		if err := releaseLock.Run(releaseCtx, s.client, []string{key}, token).Err(); err != nil && !errors.Is(err, redis.Nil) {
			return fmt.Errorf("redis: release join lock %s: %w", roomID, err)
		}
		return nil
	}
	return release, true, nil
}

// releaseLock deletes a lock key only when the stored token matches, making
// release safe against a lock that already timed out and was re-acquired.
var releaseLock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

func toCached(s ports.RoomSummary) cachedRoom {
	return cachedRoom{
		ID:           s.ID,
		OwnerID:      s.OwnerID,
		Status:       string(s.Status),
		CurrentRound: s.CurrentRound,
		MaxRounds:    s.MaxRounds,
		PlayerCount:  s.PlayerCount,
		MaxPlayers:   s.MaxPlayers,
		CreatedAt:    s.CreatedAt,
	}
}

func fromCached(c cachedRoom) ports.RoomSummary {
	return ports.RoomSummary{
		ID:           c.ID,
		OwnerID:      c.OwnerID,
		Status:       game.RoomStatus(c.Status),
		CurrentRound: c.CurrentRound,
		MaxRounds:    c.MaxRounds,
		PlayerCount:  c.PlayerCount,
		MaxPlayers:   c.MaxPlayers,
		CreatedAt:    c.CreatedAt,
	}
}
