package postgres_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jakhongirjp/ledgerflow/internal/domain"
	"github.com/jakhongirjp/ledgerflow/internal/ledger"
	"github.com/jakhongirjp/ledgerflow/internal/outbox"
	"github.com/jakhongirjp/ledgerflow/internal/store/postgres"
	"github.com/jakhongirjp/ledgerflow/internal/store/storetest"
	"github.com/jakhongirjp/ledgerflow/migrations"
)

// NewTestPool creates a throwaway database for one test and drops it after.
// Tests are skipped unless LEDGER_TEST_DATABASE_URL points at a server where
// the user may create databases.
func NewTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	adminURL := os.Getenv("LEDGER_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("LEDGER_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	name := "ledger_test_" + strings.ReplaceAll(uuid.NewString()[:13], "-", "")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
		admin.Close()
	})
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) ledger.Store { return postgres.New(NewTestPool(t)) })
}

func TestMigrationsAreIdempotent(t *testing.T) {
	pool := NewTestPool(t)
	if err := migrations.Up(context.Background(), pool); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestPostingWritesOutboxEvent(t *testing.T) {
	ctx := context.Background()
	pool := NewTestPool(t)
	s := ledger.NewService(postgres.New(pool), nil)
	a, _ := s.CreateAccount(ctx, ledger.CreateAccountInput{Name: "a", Type: "asset", Currency: "USD", AllowNegative: true})
	b, _ := s.CreateAccount(ctx, ledger.CreateAccountInput{Name: "b", Type: "equity", Currency: "USD"})
	tx, _, err := s.PostTransaction(ctx, ledger.PostTransactionInput{
		IdempotencyKey: "seed",
		Entries: []domain.Entry{
			{AccountID: a.ID, Direction: "debit", Amount: 10},
			{AccountID: b.ID, Direction: "credit", Amount: 10},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var eventType string
	var aggregate uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT event_type, aggregate_id FROM outbox`).Scan(&eventType, &aggregate); err != nil {
		t.Fatal(err)
	}
	if eventType != postgres.EventTransactionPosted || aggregate != tx.ID {
		t.Fatalf("outbox row = %s/%s", eventType, aggregate)
	}
}

type recordingPublisher struct{ ids []int64 }

func (p *recordingPublisher) Publish(_ context.Context, e outbox.Event) error {
	p.ids = append(p.ids, e.ID)
	return nil
}

func TestOutboxRelayPublishesOnce(t *testing.T) {
	ctx := context.Background()
	pool := NewTestPool(t)
	s := ledger.NewService(postgres.New(pool), nil)
	a, _ := s.CreateAccount(ctx, ledger.CreateAccountInput{Name: "a", Type: "asset", Currency: "USD", AllowNegative: true})
	b, _ := s.CreateAccount(ctx, ledger.CreateAccountInput{Name: "b", Type: "equity", Currency: "USD"})
	for i := range 3 {
		_, _, err := s.PostTransaction(ctx, ledger.PostTransactionInput{
			IdempotencyKey: fmt.Sprintf("k%d", i),
			Entries: []domain.Entry{
				{AccountID: a.ID, Direction: "debit", Amount: 1},
				{AccountID: b.ID, Direction: "credit", Amount: 1},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	pub := &recordingPublisher{}
	r := &outbox.Relay{Source: outbox.PostgresSource{Pool: pool}, Publisher: pub, BatchSize: 2}
	for range 3 {
		if _, err := r.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(pub.ids) != 3 {
		t.Fatalf("published %v, want 3 events exactly once", pub.ids)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d events still pending", pending)
	}
}
