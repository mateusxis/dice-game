// Package config loads runtime settings from the environment. Everything the
// service needs is expressed as an env var so the same binary runs locally and
// in Docker with no code change.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// Env is "development", "test" or "production"; it only affects logging.
	Env string
	// HTTPPort is the port the REST/WebSocket server listens on.
	HTTPPort string
	// DatabaseURL is a libpq/pgx connection string.
	DatabaseURL string
	// RedisAddr is host:port; RedisPassword and RedisDB are optional.
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	// JWTSecret signs access tokens. Required in every environment.
	JWTSecret string
	// JWTTTL is how long an issued access token stays valid.
	JWTTTL time.Duration
	// BettingWindow is the length of a round's betting period.
	BettingWindow time.Duration
	// BcryptCost is the bcrypt work factor for password hashing.
	BcryptCost int
	// ShutdownTimeout bounds graceful HTTP shutdown.
	ShutdownTimeout time.Duration
	// RunMigrations toggles the in-process migration step at boot.
	RunMigrations bool
}

// Defaults applied when the corresponding env var is unset.
const (
	defaultEnv             = "development"
	defaultHTTPPort        = "8080"
	defaultDatabaseURL     = "postgres://cassino:cassino@localhost:5432/cassino?sslmode=disable"
	defaultRedisAddr       = "localhost:6379"
	defaultJWTTTL          = 24 * time.Hour
	defaultBettingWindow   = 15 * time.Second
	defaultBcryptCost      = 12
	defaultShutdownTimeout = 15 * time.Second
)

// ErrMissingJWTSecret is returned when JWT_SECRET is unset or blank.
var ErrMissingJWTSecret = errors.New("config: JWT_SECRET is required")

// Load reads the configuration from the process environment, applying
// development-friendly defaults and validating what cannot be defaulted.
func Load() (Config, error) {
	cfg := Config{
		Env:             getString("APP_ENV", defaultEnv),
		HTTPPort:        getString("HTTP_PORT", defaultHTTPPort),
		DatabaseURL:     getString("DATABASE_URL", defaultDatabaseURL),
		RedisAddr:       getString("REDIS_ADDR", defaultRedisAddr),
		RedisPassword:   getString("REDIS_PASSWORD", ""),
		JWTSecret:       getString("JWT_SECRET", ""),
		ShutdownTimeout: defaultShutdownTimeout,
	}

	var err error
	if cfg.RedisDB, err = getInt("REDIS_DB", 0); err != nil {
		return Config{}, err
	}
	if cfg.JWTTTL, err = getDuration("JWT_TTL", defaultJWTTTL); err != nil {
		return Config{}, err
	}
	if cfg.BettingWindow, err = getDuration("BETTING_WINDOW", defaultBettingWindow); err != nil {
		return Config{}, err
	}
	if cfg.BcryptCost, err = getInt("BCRYPT_COST", defaultBcryptCost); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = getDuration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RunMigrations, err = getBool("RUN_MIGRATIONS", true); err != nil {
		return Config{}, err
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.JWTSecret) == "" {
		return ErrMissingJWTSecret
	}
	if c.BettingWindow <= 0 {
		return fmt.Errorf("config: BETTING_WINDOW must be positive, got %s", c.BettingWindow)
	}
	if c.JWTTTL <= 0 {
		return fmt.Errorf("config: JWT_TTL must be positive, got %s", c.JWTTTL)
	}
	if c.BcryptCost < 4 || c.BcryptCost > 31 {
		return fmt.Errorf("config: BCRYPT_COST must be between 4 and 31, got %d", c.BcryptCost)
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("config: DATABASE_URL is required")
	}
	if strings.TrimSpace(c.RedisAddr) == "" {
		return errors.New("config: REDIS_ADDR is required")
	}
	return nil
}

// Addr is the listen address for the HTTP server.
func (c Config) Addr() string { return ":" + c.HTTPPort }

func getString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func getInt(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer: %w", key, err)
	}
	return v, nil
}

func getBool(key string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean: %w", key, err)
	}
	return v, nil
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration such as 15s: %w", key, err)
	}
	return v, nil
}
