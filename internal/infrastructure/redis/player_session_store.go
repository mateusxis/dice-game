package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mateusxis/cassino/internal/application/ports"
)

// PlayerSessionStore enforces "a player is in at most one room at a time".
// The binding is a SET NX key, so two concurrent join attempts from the same
// account cannot both succeed. PostgreSQL still holds the authoritative
// membership rows; this is the fast, race-free gate in front of them.
type PlayerSessionStore struct {
	client *redis.Client
}

var _ ports.PlayerSessionStore = (*PlayerSessionStore)(nil)

// NewPlayerSessionStore builds the session store over a Redis client.
func NewPlayerSessionStore(client *redis.Client) *PlayerSessionStore {
	return &PlayerSessionStore{client: client}
}

// BindPlayerToRoom claims the player for roomID. It returns bound=false when
// the player is already bound somewhere — including to the same room, which
// callers treat as "already joined". The TTL bounds the damage of a crashed
// process leaving a stale binding behind.
func (s *PlayerSessionStore) BindPlayerToRoom(ctx context.Context, playerID, roomID string, ttl time.Duration) (bool, error) {
	ok, err := s.client.SetNX(ctx, keyActivePlay+playerID, roomID, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redis: bind player %s: %w", playerID, err)
	}
	return ok, nil
}

// ActiveRoom returns the room the player is bound to, or "" when free.
func (s *PlayerSessionStore) ActiveRoom(ctx context.Context, playerID string) (string, error) {
	roomID, err := s.client.Get(ctx, keyActivePlay+playerID).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil
		}
		return "", fmt.Errorf("redis: active room for %s: %w", playerID, err)
	}
	return roomID, nil
}

// ReleasePlayer clears the binding when the player leaves or the room closes.
func (s *PlayerSessionStore) ReleasePlayer(ctx context.Context, playerID string) error {
	if err := s.client.Del(ctx, keyActivePlay+playerID).Err(); err != nil {
		return fmt.Errorf("redis: release player %s: %w", playerID, err)
	}
	return nil
}
