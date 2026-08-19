//go:build integration

// Package integration is a black-box suite that boots the real API
// in-process (the exact wiring cmd/api/main.go uses, via internal/bootstrap)
// against live PostgreSQL and Redis, and exercises it purely through HTTP and
// WebSocket — the same way a real client would. It never imports an
// application/domain package to reach in and cheat; every assertion either
// reads the HTTP/WS response or queries the database directly to check what
// actually got persisted.
//
// Run with:
//
//	docker compose up -d postgres redis
//	go test -tags integration ./...
//	docker compose down -v
//
// The suite creates and migrates its own database (default name
// cassino_integration_test, dropped and recreated fresh at the start of the
// run) so it never touches a developer's cassino database, and uses Redis
// logical DB 15 (flushed at the start of the run) for the same reason.
// audit_logs cannot be truncated (it is append-only by trigger), which is
// exactly why the suite recreates the whole database up front instead of
// trying to clean individual tables between tests.
//
// If Postgres or Redis is not reachable at the configured address, every
// test in this package calls t.Skip with a clear message rather than
// failing — see requireDeps below.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/mateusxis/cassino/internal/bootstrap"
	"github.com/mateusxis/cassino/internal/infrastructure/config"
)

const (
	testDBName    = "cassino_integration_test"
	testRedisDB   = 15
	testJWTSecret = "integration-test-secret-do-not-use-in-production"
	// testBettingWindow is deliberately short so a full round settles in test
	// time instead of the product's real 15s.
	testBettingWindow = 2 * time.Second
	// dialCheckTimeout bounds the initial reachability probe so a missing
	// dependency fails fast with a clear message instead of hanging inside
	// pgx/redis's own (much longer) internal timeouts.
	dialCheckTimeout = 2 * time.Second
)

var (
	baseURL   string
	wsBaseURL string

	app    *bootstrap.App
	server *httptest.Server

	// setupErr is nil on a healthy setup. Every test calls requireDeps(t)
	// first, which skips (not fails) when this is set — the whole point
	// being a missing Postgres/Redis produces one clear skip reason per
	// test rather than a wall of confusing connection-refused failures.
	setupErr error
)

func adminDSN() string {
	return getenv("TEST_POSTGRES_ADMIN_DSN", "postgres://cassino:cassino@localhost:5432/postgres?sslmode=disable")
}

func testDSN() string {
	return getenv("TEST_DATABASE_URL", fmt.Sprintf("postgres://cassino:cassino@localhost:5432/%s?sslmode=disable", testDBName))
}

func testRedisAddr() string {
	return getenv("TEST_REDIS_ADDR", "localhost:6379")
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	if err := setup(ctx); err != nil {
		setupErr = err
		fmt.Fprintf(os.Stderr, "\nintegration: setup failed, every test in this package will skip: %v\n\n", err)
		fmt.Fprintf(os.Stderr, "integration: expected dependencies — run `docker compose up -d postgres redis` first (or point\n")
		fmt.Fprintf(os.Stderr, "TEST_POSTGRES_ADMIN_DSN / TEST_DATABASE_URL / TEST_REDIS_ADDR at an already-running instance).\n\n")
		os.Exit(m.Run())
	}
	code := m.Run()
	teardown(ctx)
	os.Exit(code)
}

func setup(ctx context.Context) error {
	if err := dialCheck(adminDSN()); err != nil {
		return fmt.Errorf("postgres unreachable: %w", err)
	}
	if err := dialCheck("tcp://" + testRedisAddr()); err != nil {
		return fmt.Errorf("redis unreachable: %w", err)
	}

	if err := recreateDatabase(ctx, adminDSN(), testDBName); err != nil {
		return fmt.Errorf("recreate test database: %w", err)
	}

	cfg := config.Config{
		Env:             "test",
		HTTPPort:        "0",
		DatabaseURL:     testDSN(),
		RedisAddr:       testRedisAddr(),
		RedisPassword:   "",
		RedisDB:         testRedisDB,
		JWTSecret:       testJWTSecret,
		JWTTTL:          time.Hour,
		BettingWindow:   testBettingWindow,
		BcryptCost:      4, // fast hashing; production defaults to 12
		ShutdownTimeout: 5 * time.Second,
		RunMigrations:   true,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if getenv("INTEGRATION_VERBOSE", "") != "" {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	built, err := bootstrap.Build(ctx, cfg, logger, "integration-test")
	if err != nil {
		return fmt.Errorf("bootstrap.Build: %w", err)
	}
	app = built

	// A previous failed run may have left stray keys in this logical DB;
	// start from a known-empty cache/coordination state.
	if err := app.Redis.FlushDB(ctx).Err(); err != nil {
		return fmt.Errorf("flush test redis db: %w", err)
	}

	server = httptest.NewServer(app.Router)
	baseURL = server.URL
	wsBaseURL = strings.Replace(strings.Replace(baseURL, "http://", "ws://", 1), "https://", "wss://", 1)

	return nil
}

func teardown(ctx context.Context) {
	if server != nil {
		server.Close()
	}
	if app != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		app.Engine.Shutdown(shutdownCtx)
		app.Close()
	}
}

// requireDeps skips the calling test with a clear reason when setup failed,
// so `go test -tags integration ./...` without Postgres/Redis running
// produces readable skips instead of a wall of connection-refused failures.
func requireDeps(t *testing.T) {
	t.Helper()
	if setupErr != nil {
		t.Skipf("integration test dependencies unavailable: %v", setupErr)
	}
}

// dialCheck opens and immediately closes a TCP connection to addr (given as
// a bare "host:port", or a DSN URL from which host:port is extracted) to
// fail fast and legibly when a dependency is not listening at all.
func dialCheck(addr string) error {
	host := addr
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return fmt.Errorf("parse %q: %w", addr, err)
		}
		host = u.Host
	} else {
		host = strings.TrimPrefix(addr, "tcp://")
	}
	conn, err := net.DialTimeout("tcp", host, dialCheckTimeout)
	if err != nil {
		return fmt.Errorf("dial %s: %w", host, err)
	}
	return conn.Close()
}

// recreateDatabase drops (if present) and recreates dbName against an admin
// connection, giving the suite a byte-for-byte fresh schema every run —
// simpler and more reliable than truncating tables one by one, and the only
// option at all for audit_logs, which rejects TRUNCATE by trigger.
func recreateDatabase(ctx context.Context, adminDSN, dbName string) error {
	db, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("ping admin connection: %w", err)
	}

	// WITH (FORCE) (PostgreSQL 13+) drops the database even if this suite's
	// own previous run left connections open in it (e.g. a killed test
	// binary). dbName is a compile-time constant, not user input.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(dbName))); err != nil {
		return fmt.Errorf("drop database %s: %w", dbName, err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(dbName))); err != nil {
		return fmt.Errorf("create database %s: %w", dbName, err)
	}
	return nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
