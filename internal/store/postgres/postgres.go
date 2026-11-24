// Package postgres implements ledger.Store on PostgreSQL using pgx.
//
// Concurrency model: PostTransaction locks every referenced account row with
// SELECT ... FOR UPDATE in primary-key order, so concurrent transactions over
// overlapping accounts serialize without deadlocks. Balances are updated and
// the outbox event is written in the same database transaction.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jakhongirjp/ledgerflow/internal/domain"
)

// EventTransactionPosted is the outbox event type emitted for every posting.
const EventTransactionPosted = "ledger.transaction.posted"

// Store is a PostgreSQL-backed ledger store.
type Store struct {
	pool *pgxpool.Pool
}

// New wraps an existing pool. Call migrations.Up before using it.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) CreateAccount(ctx context.Context, a domain.Account) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO accounts (id, name, type, currency, allow_negative, balance, version, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		a.ID, a.Name, a.Type, a.Currency, a.AllowNegative, a.Balance, a.Version, a.CreatedAt)
	return err
}

const accountCols = `id, name, type, currency, allow_negative, balance, version, created_at`

func scanAccount(row pgx.Row) (domain.Account, error) {
	var a domain.Account
	err := row.Scan(&a.ID, &a.Name, &a.Type, &a.Currency, &a.AllowNegative, &a.Balance, &a.Version, &a.CreatedAt)
	a.CreatedAt = a.CreatedAt.UTC()
	return a, err
}

func (s *Store) GetAccount(ctx context.Context, id uuid.UUID) (domain.Account, error) {
	a, err := scanAccount(s.pool.QueryRow(ctx, `SELECT `+accountCols+` FROM accounts WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, fmt.Errorf("account %s: %w", id, domain.ErrNotFound)
	}
	return a, err
}

func (s *Store) ListPostings(ctx context.Context, accountID uuid.UUID, limit int) ([]domain.Posting, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT transaction_id, account_id, direction, amount, balance_after, created_at
		FROM postings WHERE account_id=$1 ORDER BY id DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Posting, error) {
		var p domain.Posting
		err := r.Scan(&p.TransactionID, &p.AccountID, &p.Direction, &p.Amount, &p.BalanceAfter, &p.CreatedAt)
		p.CreatedAt = p.CreatedAt.UTC()
		return p, err
	})
}

func (s *Store) PostTransaction(ctx context.Context, tx domain.Transaction) (domain.Transaction, bool, error) {
	// Fast path: an idempotent replay does not need to take any locks.
	if existing, err := s.findByKey(ctx, tx.IdempotencyKey); err == nil {
		return replay(existing, tx)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.Transaction{}, false, err
	}

	created, conflict, err := s.post(ctx, &tx)
	if err != nil {
		return domain.Transaction{}, false, err
	}
	if conflict {
		// Another request with the same key committed first.
		existing, err := s.findByKey(ctx, tx.IdempotencyKey)
		if err != nil {
			return domain.Transaction{}, false, err
		}
		return replay(existing, tx)
	}
	return created, true, nil
}

func replay(existing, incoming domain.Transaction) (domain.Transaction, bool, error) {
	if existing.RequestHash != incoming.RequestHash {
		return domain.Transaction{}, false, domain.ErrIdempotencyConflict
	}
	return existing, false, nil
}

func (s *Store) post(ctx context.Context, tx *domain.Transaction) (domain.Transaction, bool, error) {
	dbtx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Transaction{}, false, err
	}
	defer dbtx.Rollback(ctx) //nolint:errcheck // no-op after commit

	ids := tx.AccountIDs()
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	rows, err := dbtx.Query(ctx, `SELECT `+accountCols+` FROM accounts WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return domain.Transaction{}, false, err
	}
	locked, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Account, error) { return scanAccount(r) })
	if err != nil {
		return domain.Transaction{}, false, err
	}
	accounts := make(map[uuid.UUID]*domain.Account, len(locked))
	for i := range locked {
		accounts[locked[i].ID] = &locked[i]
	}

	postings, err := domain.ApplyTransaction(tx, accounts, tx.CreatedAt)
	if err != nil {
		return domain.Transaction{}, false, err
	}

	tag, err := dbtx.Exec(ctx, `
		INSERT INTO transactions (id, idempotency_key, request_hash, description, currency, created_at)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (idempotency_key) DO NOTHING`,
		tx.ID, tx.IdempotencyKey, tx.RequestHash, tx.Description, tx.Currency, tx.CreatedAt)
	if err != nil {
		return domain.Transaction{}, false, err
	}
	if tag.RowsAffected() == 0 {
		return domain.Transaction{}, true, nil
	}

	batch := &pgx.Batch{}
	for _, a := range accounts {
		batch.Queue(`UPDATE accounts SET balance=$2, version=$3 WHERE id=$1`, a.ID, a.Balance, a.Version)
	}
	for i, p := range postings {
		batch.Queue(`INSERT INTO postings (transaction_id, account_id, direction, amount, balance_after, position, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, p.TransactionID, p.AccountID, p.Direction, p.Amount, p.BalanceAfter, i, p.CreatedAt)
	}
	payload, err := json.Marshal(tx)
	if err != nil {
		return domain.Transaction{}, false, err
	}
	batch.Queue(`INSERT INTO outbox (aggregate_id, event_type, payload) VALUES ($1,$2,$3)`, tx.ID, EventTransactionPosted, payload)
	if err := dbtx.SendBatch(ctx, batch).Close(); err != nil {
		return domain.Transaction{}, false, err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return domain.Transaction{}, false, err
	}
	return *tx, false, nil
}

func (s *Store) findByKey(ctx context.Context, key string) (domain.Transaction, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM transactions WHERE idempotency_key=$1`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Transaction{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Transaction{}, err
	}
	return s.GetTransaction(ctx, id)
}

func (s *Store) GetTransaction(ctx context.Context, id uuid.UUID) (domain.Transaction, error) {
	var tx domain.Transaction
	err := s.pool.QueryRow(ctx, `
		SELECT id, idempotency_key, request_hash, description, currency, created_at
		FROM transactions WHERE id=$1`, id).
		Scan(&tx.ID, &tx.IdempotencyKey, &tx.RequestHash, &tx.Description, &tx.Currency, &tx.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Transaction{}, fmt.Errorf("transaction %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Transaction{}, err
	}
	tx.CreatedAt = tx.CreatedAt.UTC()
	rows, err := s.pool.Query(ctx, `SELECT account_id, direction, amount FROM postings WHERE transaction_id=$1 ORDER BY position`, id)
	if err != nil {
		return domain.Transaction{}, err
	}
	tx.Entries, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Entry, error) {
		var e domain.Entry
		return e, r.Scan(&e.AccountID, &e.Direction, &e.Amount)
	})
	return tx, err
}
