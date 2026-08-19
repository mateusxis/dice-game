// Package bootstrap is the composition root's wiring, extracted out of
// cmd/api/main.go so it can be reused by anything that needs a fully wired
// instance of the API without re-implementing dependency injection —
// currently that's just the integration test suite (integration/), which
// boots the real app in-process against a live Postgres/Redis instead of
// re-deriving the wiring against fakes.
//
// Build performs exactly what main.go used to do inline: run migrations
// (optionally), open the Postgres pool and Redis client, construct every
// adapter and use case, run startup recovery, and assemble the HTTP router
// and the round engine. Nothing about *behavior* changes here — this is a
// pure refactor that moves the wiring into a reusable function; main.go keeps
// owning process-level concerns (signal handling, listening, graceful
// shutdown).
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	authapp "github.com/mateusxis/cassino/internal/application/auth"
	gameapp "github.com/mateusxis/cassino/internal/application/game"
	walletapp "github.com/mateusxis/cassino/internal/application/wallet"
	"github.com/mateusxis/cassino/internal/infrastructure/auth"
	"github.com/mateusxis/cassino/internal/infrastructure/clock"
	"github.com/mateusxis/cassino/internal/infrastructure/config"
	"github.com/mateusxis/cassino/internal/infrastructure/dice"
	"github.com/mateusxis/cassino/internal/infrastructure/postgres"
	redisinfra "github.com/mateusxis/cassino/internal/infrastructure/redis"
	"github.com/mateusxis/cassino/internal/interfaces/rest"
	"github.com/mateusxis/cassino/internal/interfaces/ws"
)

// App is a fully wired instance of the API: an HTTP router (REST + the /ws
// upgrade) backed by a live Postgres pool and Redis client, plus the round
// engine that must be told to Shutdown after the HTTP server stops accepting
// connections.
type App struct {
	Config config.Config
	Logger *slog.Logger

	Pool  *pgxpool.Pool
	Redis *goredis.Client

	Router http.Handler
	Engine *gameapp.Engine

	// SchemaVersion is the migration version applied at boot, 0 when
	// RunMigrations was false.
	SchemaVersion uint
}

// Build wires every dependency and returns a ready-to-serve App. Callers own
// the returned App's lifecycle: stop accepting HTTP traffic, then call
// Engine.Shutdown(ctx) to abort/refund any live rounds, then Close() to
// release the Postgres pool and Redis client.
func Build(ctx context.Context, cfg config.Config, logger *slog.Logger, version string) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}

	var schemaVersion uint
	if cfg.RunMigrations {
		v, err := postgres.Migrate(cfg.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("migrations: %w", err)
		}
		schemaVersion = v
		logger.Info("migrations applied", "schema_version", schemaVersion)
	}

	pool, err := openPool(ctx, cfg)
	if err != nil {
		return nil, err
	}
	logger.Info("connected to postgres")

	redisClient, err := redisinfra.NewClient(ctx, cfg)
	if err != nil {
		pool.Close()
		return nil, err
	}
	logger.Info("connected to redis")

	// --- adapters ------------------------------------------------------
	sysClock := clock.NewSystem()
	ids := auth.NewUUIDGenerator()
	hasher := auth.NewBcryptHasher(cfg.BcryptCost)
	tokens := auth.NewJWTService(cfg.JWTSecret, cfg.JWTTTL)

	roller := dice.NewCryptoRoller()

	txManager := postgres.NewTxManager(pool)
	playerRepo := postgres.NewPlayerRepository(pool)
	transactionRepo := postgres.NewTransactionRepository(pool)
	roomRepo := postgres.NewRoomRepository(pool)
	betRepo := postgres.NewBetRepository(pool)
	roundRepo := postgres.NewRoundRepository(pool, betRepo)
	auditRepo := postgres.NewAuditRepository(pool)
	sessionStore := redisinfra.NewPlayerSessionStore(redisClient)
	roomStateStore := redisinfra.NewRoomStateStore(redisClient)

	// --- use cases -------------------------------------------------------
	registerUC := authapp.NewRegisterUseCase(playerRepo, hasher, ids, sysClock)
	loginUC := authapp.NewLoginUseCase(playerRepo, hasher, tokens)
	depositUC := walletapp.NewDepositUseCase(txManager, playerRepo, transactionRepo, ids, sysClock)
	withdrawUC := walletapp.NewWithdrawUseCase(txManager, playerRepo, transactionRepo, roomRepo, sessionStore, ids, sysClock)
	balanceUC := walletapp.NewGetBalanceUseCase(playerRepo)

	createRoomUC := gameapp.NewCreateRoomUseCase(txManager, roomRepo, roomStateStore, sessionStore, ids, sysClock)
	listRoomsUC := gameapp.NewListOpenRoomsUseCase(roomRepo, roomStateStore)
	joinRoomUC := gameapp.NewJoinRoomUseCase(txManager, roomRepo, roomStateStore, sessionStore, sysClock)
	closeRoomUC := gameapp.NewCloseRoomUseCase(txManager, roomRepo, roundRepo, playerRepo, roomStateStore, sessionStore, sysClock)
	startRoundUC := gameapp.NewStartRoundUseCase(txManager, roomRepo, roundRepo, roomStateStore, ids, sysClock, cfg.BettingWindow)
	placeBetUC := gameapp.NewPlaceBetUseCase(txManager, roomRepo, roundRepo, betRepo, playerRepo, transactionRepo, ids, sysClock)
	settleRoundUC := gameapp.NewSettleRoundUseCase(txManager, roomRepo, roundRepo, betRepo, playerRepo, transactionRepo, roomStateStore, sessionStore, roller, ids, sysClock)
	abortRoomUC := gameapp.NewAbortRoomUseCase(txManager, roomRepo, roundRepo, betRepo, playerRepo, transactionRepo, roomStateStore, sessionStore, ids, sysClock)
	roomStateUC := gameapp.NewRoomStateUseCase(roomRepo, roundRepo)

	// Recovery before anything can serve: a room left open by a previous
	// process has no goroutine to settle it, so it is closed and its open
	// stakes refunded. See RecoverRoomsUseCase for the trade-off.
	recovery, err := gameapp.NewRecoverRoomsUseCase(roomRepo, abortRoomUC, logger).Execute(ctx)
	if err != nil {
		logger.Error("startup recovery failed", "error", err)
	} else if recovery.RoomsFound > 0 {
		logger.Info("startup recovery complete",
			"rooms_found", recovery.RoomsFound, "rooms_closed", recovery.RoomsClosed, "failures", recovery.Failures)
	}

	// --- websocket hub + round engine ------------------------------------
	hub := ws.NewHub(logger)
	engine := gameapp.NewEngine(gameapp.EngineOptions{
		StartRound:  startRoundUC,
		PlaceBet:    placeBetUC,
		SettleRound: settleRoundUC,
		CloseRoom:   closeRoomUC,
		AbortRoom:   abortRoomUC,
		Notifier:    hub,
		Clock:       sysClock,
		Timers:      gameapp.SystemTimers(),
		Logger:      logger,
	})
	wsHandler := ws.NewHandler(ws.HandlerOptions{
		Tokens:    tokens,
		Hub:       hub,
		Join:      joinRoomUC,
		State:     roomStateUC,
		Rounds:    engine,
		AuditRepo: auditRepo,
		Clock:     sysClock,
		IDs:       ids,
		Logger:    logger,
	})

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

		CreateRoom: createRoomUC,
		ListRooms:  listRoomsUC,
		CloseRoom:  engine,
		WSHandler:  wsHandler,

		Tokens: tokens,

		AuditRepo: auditRepo,
		Clock:     sysClock,
		IDs:       ids,
		Logger:    logger,
	})

	return &App{
		Config:        cfg,
		Logger:        logger,
		Pool:          pool,
		Redis:         redisClient,
		Router:        router,
		Engine:        engine,
		SchemaVersion: schemaVersion,
	}, nil
}

// Close releases the Postgres pool and Redis client. It does not touch the
// round engine — callers must call Engine.Shutdown(ctx) themselves first, at
// the point in their own shutdown sequence where it's safe to abort live
// rounds (normally right after the HTTP server stops accepting connections).
func (a *App) Close() {
	if a.Pool != nil {
		a.Pool.Close()
	}
	if a.Redis != nil {
		_ = a.Redis.Close()
	}
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
