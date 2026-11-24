// Package ledger holds the application service that coordinates the domain
// model with a storage backend.
package ledger

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/jakhongirjp/ledgerflow/internal/domain"
)

// Store persists accounts and transactions. PostTransaction must be atomic:
// it locks the referenced accounts, calls domain.ApplyTransaction, writes the
// postings plus an outbox event, and commits, or changes nothing.
type Store interface {
	CreateAccount(ctx context.Context, a domain.Account) error
	GetAccount(ctx context.Context, id uuid.UUID) (domain.Account, error)
	ListPostings(ctx context.Context, accountID uuid.UUID, limit int) ([]domain.Posting, error)
	// PostTransaction returns the stored transaction and whether it was
	// newly created (false means an idempotent replay).
	PostTransaction(ctx context.Context, tx domain.Transaction) (domain.Transaction, bool, error)
	GetTransaction(ctx context.Context, id uuid.UUID) (domain.Transaction, error)
	Ping(ctx context.Context) error
}

// Service is the use-case layer used by transports.
type Service struct {
	store Store
	now   func() time.Time
}

// NewService builds a Service. now may be nil to use time.Now.
func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

// CreateAccountInput is the payload for opening an account.
type CreateAccountInput struct {
	Name          string             `json:"name"`
	Type          domain.AccountType `json:"type"`
	Currency      string             `json:"currency"`
	AllowNegative bool               `json:"allow_negative"`
}

// CreateAccount opens a new account with a zero balance.
func (s *Service) CreateAccount(ctx context.Context, in CreateAccountInput) (domain.Account, error) {
	acc, err := domain.NewAccount(in.Name, in.Type, in.Currency, in.AllowNegative, s.now())
	if err != nil {
		return domain.Account{}, err
	}
	if err := s.store.CreateAccount(ctx, acc); err != nil {
		return domain.Account{}, err
	}
	return acc, nil
}

// GetAccount returns an account by ID.
func (s *Service) GetAccount(ctx context.Context, id uuid.UUID) (domain.Account, error) {
	return s.store.GetAccount(ctx, id)
}

// ListPostings returns the most recent postings for an account, newest first.
func (s *Service) ListPostings(ctx context.Context, id uuid.UUID, limit int) ([]domain.Posting, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if _, err := s.store.GetAccount(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListPostings(ctx, id, limit)
}

// PostTransactionInput is the payload for posting a transaction.
type PostTransactionInput struct {
	IdempotencyKey string         `json:"-"`
	Description    string         `json:"description"`
	Entries        []domain.Entry `json:"entries"`
}

// PostTransaction validates and atomically posts a balanced transaction.
func (s *Service) PostTransaction(ctx context.Context, in PostTransactionInput) (domain.Transaction, bool, error) {
	tx := domain.Transaction{
		ID:             uuid.New(),
		IdempotencyKey: in.IdempotencyKey,
		RequestHash:    domain.RequestHash(in.Description, in.Entries),
		Description:    in.Description,
		Entries:        in.Entries,
		CreatedAt:      s.now().UTC(),
	}
	if err := tx.Validate(); err != nil {
		return domain.Transaction{}, false, err
	}
	return s.store.PostTransaction(ctx, tx)
}

// GetTransaction returns a transaction by ID.
func (s *Service) GetTransaction(ctx context.Context, id uuid.UUID) (domain.Transaction, error) {
	return s.store.GetTransaction(ctx, id)
}

// Ready reports whether the backing store is reachable.
func (s *Service) Ready(ctx context.Context) error { return s.store.Ping(ctx) }
