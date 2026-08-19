// Command api is the process entry point: it loads configuration, builds a
// fully wired App via internal/bootstrap, serves the HTTP API and drives
// graceful shutdown. All dependency injection lives in internal/bootstrap so
// it can be reused by the integration test suite; this file only owns
// process-level concerns (signals, listening, shutdown sequencing).
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

	"github.com/mateusxis/cassino/internal/bootstrap"
	"github.com/mateusxis/cassino/internal/infrastructure/config"
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

	app, err := bootstrap.Build(ctx, cfg, logger, version)
	if err != nil {
		return err
	}
	defer app.Close()

	server := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           app.Router,
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
	// Sockets are gone; now stop the round loops. A room with an open betting
	// window is aborted and its stakes refunded rather than settled early.
	app.Engine.Shutdown(shutdownCtx)
	logger.Info("shutdown complete")
	return nil
}

func newLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
