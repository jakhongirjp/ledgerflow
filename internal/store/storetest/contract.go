// Package storetest is a contract test suite every ledger.Store must pass.
// Running the same suite against the in-memory and Postgres backends keeps
// their behaviour identical.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jakhongirjp/ledgerflow/internal/domain"
	"github.com/jakhongirjp/ledgerflow/internal/ledger"
)

// Run executes the contract suite. newStore must return an isolated store.
func Run(t *testing.T, newStore func(t *testing.T) ledger.Store) {
	t.Run("AccountRoundTrip", func(t *testing.T) { testAccountRoundTrip(t, newStore(t)) })
	t.Run("PostAndReplay", func(t *testing.T) { testPostAndReplay(t, newStore(t)) })
	t.Run("IdempotencyConflict", func(t *testing.T) { testIdempotencyConflict(t, newStore(t)) })
	t.Run("InsufficientFundsLeavesNoTrace", func(t *testing.T) { testInsufficientFunds(t, newStore(t)) })
	t.Run("ConcurrentTransfersNeverOverdraw", func(t *testing.T) { testConcurrency(t, newStore(t)) })
	t.Run("NotFound", func(t *testing.T) { testNotFound(t, newStore(t)) })
}

func svc(s ledger.Store) *ledger.Service { return ledger.NewService(s, nil) }

func open(t *testing.T, s *ledger.Service, typ domain.AccountType, allowNeg bool) domain.Account {
	t.Helper()
	a, err := s.CreateAccount(context.Background(), ledger.CreateAccountInput{
		Name: "acc-" + uuid.NewString()[:8], Type: typ, Currency: "USD", AllowNegative: allowNeg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// fund moves amount from a negative-allowed funding asset into a liability wallet.
func fund(t *testing.T, s *ledger.Service, wallet domain.Account, amount int64) {
	t.Helper()
	bank := open(t, s, domain.Asset, true)
	_, _, err := s.PostTransaction(context.Background(), ledger.PostTransactionInput{
		IdempotencyKey: uuid.NewString(),
		Entries: []domain.Entry{
			{AccountID: bank.ID, Direction: domain.Debit, Amount: amount},
			{AccountID: wallet.ID, Direction: domain.Credit, Amount: amount},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func transfer(from, to uuid.UUID, amount int64, key string) ledger.PostTransactionInput {
	return ledger.PostTransactionInput{
		IdempotencyKey: key,
		Description:    "transfer",
		Entries: []domain.Entry{
			{AccountID: from, Direction: domain.Debit, Amount: amount},
			{AccountID: to, Direction: domain.Credit, Amount: amount},
		},
	}
}

func balance(t *testing.T, s *ledger.Service, id uuid.UUID) int64 {
	t.Helper()
	a, err := s.GetAccount(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Balance
}

func testAccountRoundTrip(t *testing.T, st ledger.Store) {
	s := svc(st)
	a := open(t, s, domain.Liability, false)
	got, err := s.GetAccount(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != a.Name || got.Type != a.Type || got.Currency != "USD" || got.Balance != 0 {
		t.Fatalf("got %+v, want %+v", got, a)
	}
	// Postgres stores microseconds, so allow for truncation.
	if got.CreatedAt.Sub(a.CreatedAt).Abs() > time.Millisecond {
		t.Fatalf("created_at mismatch %v vs %v", got.CreatedAt, a.CreatedAt)
	}
}

func testPostAndReplay(t *testing.T, st ledger.Store) {
	ctx := context.Background()
	s := svc(st)
	alice, bob := open(t, s, domain.Liability, false), open(t, s, domain.Liability, false)
	fund(t, s, alice, 1000)

	tx, created, err := s.PostTransaction(ctx, transfer(alice.ID, bob.ID, 300, "pay-1"))
	if err != nil || !created {
		t.Fatalf("first post: created=%v err=%v", created, err)
	}
	replay, created, err := s.PostTransaction(ctx, transfer(alice.ID, bob.ID, 300, "pay-1"))
	if err != nil || created {
		t.Fatalf("replay: created=%v err=%v", created, err)
	}
	if replay.ID != tx.ID {
		t.Fatalf("replay returned a different transaction: %s vs %s", replay.ID, tx.ID)
	}
	if a, b := balance(t, s, alice.ID), balance(t, s, bob.ID); a != 700 || b != 300 {
		t.Fatalf("balances alice=%d bob=%d, want 700/300", a, b)
	}

	got, err := s.GetTransaction(ctx, tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Currency != "USD" || len(got.Entries) != 2 || got.Description != "transfer" {
		t.Fatalf("unexpected stored tx %+v", got)
	}

	postings, err := s.ListPostings(ctx, alice.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(postings) != 2 || postings[0].BalanceAfter != 700 || postings[1].BalanceAfter != 1000 {
		t.Fatalf("unexpected postings %+v", postings)
	}
}

func testIdempotencyConflict(t *testing.T, st ledger.Store) {
	ctx := context.Background()
	s := svc(st)
	alice, bob := open(t, s, domain.Liability, false), open(t, s, domain.Liability, false)
	fund(t, s, alice, 1000)
	if _, _, err := s.PostTransaction(ctx, transfer(alice.ID, bob.ID, 100, "k")); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.PostTransaction(ctx, transfer(alice.ID, bob.ID, 999, "k"))
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func testInsufficientFunds(t *testing.T, st ledger.Store) {
	ctx := context.Background()
	s := svc(st)
	alice, bob := open(t, s, domain.Liability, false), open(t, s, domain.Liability, false)
	fund(t, s, alice, 50)
	_, _, err := s.PostTransaction(ctx, transfer(alice.ID, bob.ID, 51, "too-much"))
	if !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Fatalf("expected insufficient funds, got %v", err)
	}
	if a, b := balance(t, s, alice.ID), balance(t, s, bob.ID); a != 50 || b != 0 {
		t.Fatalf("balances changed: alice=%d bob=%d", a, b)
	}
	// The key was not consumed, so a corrected retry succeeds.
	if _, created, err := s.PostTransaction(ctx, transfer(alice.ID, bob.ID, 50, "too-much")); err != nil || !created {
		t.Fatalf("retry: created=%v err=%v", created, err)
	}
}

func testConcurrency(t *testing.T, st ledger.Store) {
	ctx := context.Background()
	s := svc(st)
	src := open(t, s, domain.Liability, false)
	dsts := []domain.Account{open(t, s, domain.Liability, false), open(t, s, domain.Liability, false)}
	fund(t, s, src, 1000)

	const workers = 40 // each tries to move 50; only 20 can succeed
	var ok, rejected atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Alternate direction of account ordering to provoke deadlocks
			// in a naive implementation.
			dst := dsts[i%2]
			_, _, err := s.PostTransaction(ctx, transfer(src.ID, dst.ID, 50, fmt.Sprintf("c-%d", i)))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, domain.ErrInsufficientFunds):
				rejected.Add(1)
			default:
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok.Load() != 20 || rejected.Load() != 20 {
		t.Fatalf("ok=%d rejected=%d, want 20/20", ok.Load(), rejected.Load())
	}
	total := balance(t, s, src.ID) + balance(t, s, dsts[0].ID) + balance(t, s, dsts[1].ID)
	if balance(t, s, src.ID) != 0 || total != 1000 {
		t.Fatalf("money was created or destroyed: src=%d total=%d", balance(t, s, src.ID), total)
	}
}

func testNotFound(t *testing.T, st ledger.Store) {
	ctx := context.Background()
	s := svc(st)
	if _, err := s.GetAccount(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("account: %v", err)
	}
	if _, err := s.GetTransaction(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("transaction: %v", err)
	}
	a := open(t, s, domain.Liability, true)
	_, _, err := s.PostTransaction(ctx, transfer(a.ID, uuid.New(), 1, "ghost"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("post to unknown account: %v", err)
	}
}
