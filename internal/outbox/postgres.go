package outbox

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresSource claims rows with FOR UPDATE SKIP LOCKED, which lets several
// relay replicas run side by side without publishing the same row twice in
// the normal case.
type PostgresSource struct {
	Pool *pgxpool.Pool
}

func (s PostgresSource) Claim(ctx context.Context, limit int) ([]Event, func(context.Context, []int64, []int64) error, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id, aggregate_id::text, event_type, payload::text
		FROM outbox WHERE published_at IS NULL
		ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		var payload string
		err := r.Scan(&e.ID, &e.AggregateID, &e.Type, &payload)
		e.Payload = []byte(payload)
		return e, err
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}

	done := func(ctx context.Context, published, failed []int64) error {
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
		if len(published) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at=now(), attempts=attempts+1 WHERE id = ANY($1)`, published); err != nil {
				return err
			}
		}
		if len(failed) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE outbox SET attempts=attempts+1 WHERE id = ANY($1)`, failed); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	return events, done, nil
}
