// Command api is the composition root: it loads configuration, opens the
// PostgreSQL and Redis connections, applies the embedded migrations and serves
// the HTTP API. Every dependency is constructed here and injected downward, so
// no inner layer ever reaches for a global.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	authapp "github.com/mateusxis/cassino/internal/application/auth"
	walletapp "github.com/mateusxis/cassino/internal/application/wallet"
	"github.com/mateusxis/cassino/internal/infrastructure/auth"
	"github.com/mateusxis/cassino/internal/infrastructure/clock"
	"github.com/mateusxis/cassino/internal/infrastructure/config"
	"github.com/mateusxis/cassino/internal/infrastructure/postgres"
	redisinfra "github.com/mateusxis/cassino/internal/infrastructure/redis"
	"github.com/mateusxis/cassino/internal/interfaces/rest"
)

// version is stamped by the build (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Env)
	slog.SetDefault(logger)
	logger.Info("starting cassino api", "version", version, "env", cfg.Env, "port", cfg.HTTPPort)

	// Signals cancel the root context, which unwinds every dependency below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Migrations run before the pool opens so the first request always meets a
	// fully-migrated schema. golang-migrate takes an advisory lock, so several
	// replicas booting at once is safe.
	if cfg.RunMigrations {
		schemaVersion, err := postgres.Migrate(cfg.DatabaseURL)
		if err != nil {
			return fmt.Errorf("migrations: %w", err)
		}
		logger.Info("migrations applied", "schema_version", schemaVersion)
	}

	pool, err := openPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("connected to postgres")

	redisClient, err := redisinfra.NewClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = redisClient.Close() }()
	logger.Info("connected to redis")

	// --- adapters ------------------------------------------------------
	sysClock := clock.NewSystem()
	ids := auth.NewUUIDGenerator()
	hasher := auth.NewBcryptHasher(cfg.BcryptCost)
	tokens := auth.NewJWTService(cfg.JWTSecret, cfg.JWTTTL)

	txManager := postgres.NewTxManager(pool)
	playerRepo := postgres.NewPlayerRepository(pool)
	transactionRepo := postgres.NewTransactionRepository(pool)
	roomRepo := postgres.NewRoomRepository(pool)
	auditRepo := postgres.NewAuditRepository(pool)
	sessionStore := redisinfra.NewPlayerSessionStore(redisClient)

	// --- use cases -------------------------------------------------------
	registerUC := authapp.NewRegisterUseCase(playerRepo, hasher, ids, sysClock)
	loginUC := authapp.NewLoginUseCase(playerRepo, hasher, tokens)
	depositUC := walletapp.NewDepositUseCase(txManager, playerRepo, transactionRepo, ids, sysClock)
	withdrawUC := walletapp.NewWithdrawUseCase(txManager, playerRepo, transactionRepo, roomRepo, sessionStore, ids, sysClock)
	balanceUC := walletapp.NewGetBalanceUseCase(playerRepo)

	router := rest.NewRouter(rest.RouterOptions{
		Version:        version,
		RequestTimeout: 30 * time.Second,
		Dependencies: []rest.Dependency{
			{Name: "postgres", Check: pingPostgres(pool)},
			{Name: "redis", Check: pingRedis(redisClient)},
		},

		RegisterUseCase: registerUC,
		LoginUseCase:    loginUC,
		DepositUseCase:  depositUC,
		WithdrawUseCase: withdrawUC,
		BalanceUseCase:  balanceUC,
		Tokens:          tokens,

		AuditRepo: auditRepo,
		Clock:     sysClock,
		IDs:       ids,
		Logger:    logger,
	})

	server := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}

func openPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	poolCfg.MaxConns = 16
	poolCfg.MinConns = 2
	poolCfg.MaxConnLifetime = time.Hour
	poolCfg.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func pingPostgres(pool *pgxpool.Pool) rest.HealthChecker {
	return func(r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		return pool.Ping(ctx)
	}
}

func pingRedis(client *goredis.Client) rest.HealthChecker {
	return func(r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		return client.Ping(ctx).Err()
	}
}

func newLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
