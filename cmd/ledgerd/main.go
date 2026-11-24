// Command ledgerd serves the ledger HTTP API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jakhongirjp/ledgerflow/internal/config"
	"github.com/jakhongirjp/ledgerflow/internal/httpapi"
	"github.com/jakhongirjp/ledgerflow/internal/ledger"
	"github.com/jakhongirjp/ledgerflow/internal/observability"
	"github.com/jakhongirjp/ledgerflow/internal/store/memory"
	"github.com/jakhongirjp/ledgerflow/internal/store/postgres"
	"github.com/jakhongirjp/ledgerflow/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("ledgerd exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var store ledger.Store
	switch cfg.Store {
	case "memory":
		logger.Warn("using in-memory store; data is lost on restart")
		store = memory.New()
	default:
		pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := migrations.Up(ctx, pool); err != nil {
			return err
		}
		store = postgres.New(pool)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.New(ledger.NewService(store, nil), logger, httpapi.NewMetrics()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
