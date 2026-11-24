// Package memory is an in-process Store used for tests and local demos.
package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/jakhongirjp/ledgerflow/internal/domain"
)

// Store is a goroutine-safe in-memory implementation of ledger.Store.
type Store struct {
	mu       sync.Mutex
	accounts map[uuid.UUID]domain.Account
	txs      map[uuid.UUID]domain.Transaction
	byKey    map[string]uuid.UUID
	postings map[uuid.UUID][]domain.Posting
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		accounts: map[uuid.UUID]domain.Account{},
		txs:      map[uuid.UUID]domain.Transaction{},
		byKey:    map[string]uuid.UUID{},
		postings: map[uuid.UUID][]domain.Posting{},
	}
}

func (s *Store) CreateAccount(_ context.Context, a domain.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[a.ID] = a
	return nil
}

func (s *Store) GetAccount(_ context.Context, id uuid.UUID) (domain.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return domain.Account{}, fmt.Errorf("account %s: %w", id, domain.ErrNotFound)
	}
	return a, nil
}

func (s *Store) ListPostings(_ context.Context, id uuid.UUID, limit int) ([]domain.Posting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.postings[id]
	out := make([]domain.Posting, 0, min(limit, len(all)))
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, all[i])
	}
	return out, nil
}

func (s *Store) PostTransaction(_ context.Context, tx domain.Transaction) (domain.Transaction, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.byKey[tx.IdempotencyKey]; ok {
		existing := s.txs[id]
		if existing.RequestHash != tx.RequestHash {
			return domain.Transaction{}, false, domain.ErrIdempotencyConflict
		}
		return existing, false, nil
	}

	working := make(map[uuid.UUID]*domain.Account, len(tx.Entries))
	for _, id := range tx.AccountIDs() {
		if a, ok := s.accounts[id]; ok {
			cp := a
			working[id] = &cp
		}
	}
	postings, err := domain.ApplyTransaction(&tx, working, tx.CreatedAt)
	if err != nil {
		return domain.Transaction{}, false, err
	}
	for id, a := range working {
		s.accounts[id] = *a
	}
	for _, p := range postings {
		s.postings[p.AccountID] = append(s.postings[p.AccountID], p)
	}
	s.txs[tx.ID] = tx
	s.byKey[tx.IdempotencyKey] = tx.ID
	return tx, true, nil
}

func (s *Store) GetTransaction(_ context.Context, id uuid.UUID) (domain.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, ok := s.txs[id]
	if !ok {
		return domain.Transaction{}, fmt.Errorf("transaction %s: %w", id, domain.ErrNotFound)
	}
	return tx, nil
}

func (s *Store) Ping(context.Context) error { return nil }
