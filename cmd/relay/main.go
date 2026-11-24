// Command relay publishes outbox events from Postgres to NATS JetStream.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jakhongirjp/ledgerflow/internal/config"
	"github.com/jakhongirjp/ledgerflow/internal/observability"
	"github.com/jakhongirjp/ledgerflow/internal/outbox"
	"github.com/jakhongirjp/ledgerflow/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("relay exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Store != "postgres" {
		return errors.New("relay requires LEDGER_STORE=postgres")
	}
	logger := observability.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrations.Up(ctx, pool); err != nil {
		return err
	}

	var pub outbox.Publisher = outbox.LogPublisher{Logger: logger}
	if cfg.NATSURL != "" {
		nc, err := nats.Connect(cfg.NATSURL, nats.Name("ledgerflow-relay"), nats.MaxReconnects(-1))
		if err != nil {
			return err
		}
		defer nc.Drain() //nolint:errcheck
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:       "LEDGER",
			Subjects:   []string{cfg.SubjectPrefix + ".>"},
			Duplicates: 2 * time.Minute,
		}); err != nil {
			return err
		}
		pub = outbox.JetStreamPublisher{JS: js, SubjectPrefix: cfg.SubjectPrefix}
		logger.Info("publishing to NATS JetStream", "url", cfg.NATSURL)
	} else {
		logger.Warn("NATS_URL not set; events will be logged only")
	}

	r := &outbox.Relay{
		Source:    outbox.PostgresSource{Pool: pool},
		Publisher: pub,
		BatchSize: cfg.RelayBatchSize,
		Interval:  cfg.RelayInterval,
		Logger:    logger,
	}
	logger.Info("relay started", "batch_size", cfg.RelayBatchSize, "interval", cfg.RelayInterval)
	return r.Run(ctx)
}
