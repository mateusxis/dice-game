// Package redis holds the cache and coordination adapters. PostgreSQL remains
// the source of truth; Redis exists to make open-room lookups cheap and to
// provide the short-lived locks that guard the 6-player seat cap and the
// one-room-per-player rule.
package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/mateusxis/cassino/internal/infrastructure/config"
)

// NewClient builds a go-redis client from configuration and verifies
// connectivity with a PING so a misconfigured deployment fails at boot rather
// than on the first request.
func NewClient(ctx context.Context, cfg config.Config) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis: ping %s: %w", cfg.RedisAddr, err)
	}
	return client, nil
}

// Key namespaces. Every key this service writes is prefixed so a shared Redis
// instance stays legible.
const (
	keyOpenRooms  = "cassino:rooms:open"     // hash: roomID -> JSON summary
	keyRoomPrefix = "cassino:room:"          // string: per-room cached summary
	keyJoinLock   = "cassino:room:joinlock:" // string: join mutex per room
	keyActivePlay = "cassino:active_player:" // string: playerID -> roomID
)
