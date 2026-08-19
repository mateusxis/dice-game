package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/infrastructure/config"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "8080", cfg.HTTPPort)
	assert.Equal(t, ":8080", cfg.Addr())
	assert.Equal(t, 15*time.Second, cfg.BettingWindow, "the spec fixes the betting window at 15 seconds")
	assert.Equal(t, 24*time.Hour, cfg.JWTTTL)
	assert.Equal(t, 12, cfg.BcryptCost)
	assert.True(t, cfg.RunMigrations)
	assert.NotEmpty(t, cfg.DatabaseURL)
	assert.NotEmpty(t, cfg.RedisAddr)
}

func TestLoadRequiresJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")

	_, err := config.Load()
	assert.ErrorIs(t, err, config.ErrMissingJWTSecret)
}

func TestLoadReadsOverrides(t *testing.T) {
	t.Setenv("JWT_SECRET", "s3cr3t")
	t.Setenv("HTTP_PORT", "9090")
	t.Setenv("BETTING_WINDOW", "5s")
	t.Setenv("JWT_TTL", "1h")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("BCRYPT_COST", "6")
	t.Setenv("RUN_MIGRATIONS", "false")

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "9090", cfg.HTTPPort)
	assert.Equal(t, 5*time.Second, cfg.BettingWindow)
	assert.Equal(t, time.Hour, cfg.JWTTTL)
	assert.Equal(t, 3, cfg.RedisDB)
	assert.Equal(t, 6, cfg.BcryptCost)
	assert.False(t, cfg.RunMigrations)
}

func TestLoadRejectsBadValues(t *testing.T) {
	t.Setenv("JWT_SECRET", "s3cr3t")

	t.Run("unparseable duration", func(t *testing.T) {
		t.Setenv("JWT_SECRET", "s3cr3t")
		t.Setenv("BETTING_WINDOW", "fifteen")
		_, err := config.Load()
		assert.Error(t, err)
	})

	t.Run("non-positive betting window", func(t *testing.T) {
		t.Setenv("JWT_SECRET", "s3cr3t")
		t.Setenv("BETTING_WINDOW", "0s")
		_, err := config.Load()
		assert.Error(t, err)
	})

	t.Run("bcrypt cost out of range", func(t *testing.T) {
		t.Setenv("JWT_SECRET", "s3cr3t")
		t.Setenv("BCRYPT_COST", "99")
		_, err := config.Load()
		assert.Error(t, err)
	})

	t.Run("non-integer redis db", func(t *testing.T) {
		t.Setenv("JWT_SECRET", "s3cr3t")
		t.Setenv("REDIS_DB", "abc")
		_, err := config.Load()
		assert.Error(t, err)
	})
}
