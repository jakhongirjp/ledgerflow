// Package outbox publishes events written by the ledger to a message broker.
//
// Delivery is at-least-once: an event is marked published only after the
// broker acknowledges it, so a crash between the two steps causes a resend.
// Consumers deduplicate on the event ID (the Nats-Msg-Id header).
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Event is a row from the outbox table.
type Event struct {
	ID          int64
	AggregateID string
	Type        string
	Payload     []byte
}

// Publisher sends one event to a broker.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Source claims unpublished events and records results. Claim must hold
// whatever locks are needed until the returned commit function runs.
type Source interface {
	Claim(ctx context.Context, limit int) (events []Event, done func(ctx context.Context, published []int64, failed []int64) error, err error)
}

// Relay moves events from Source to Publisher in batches.
type Relay struct {
	Source    Source
	Publisher Publisher
	BatchSize int
	Interval  time.Duration
	Logger    *slog.Logger
}

// RunOnce processes a single batch and returns how many events were published.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	events, done, err := r.Source.Claim(ctx, r.batchSize())
	if err != nil {
		return 0, err
	}
	var published, failed []int64
	for _, e := range events {
		if err := r.Publisher.Publish(ctx, e); err != nil {
			r.logger().Warn("publish failed", "event_id", e.ID, "err", err)
			failed = append(failed, e.ID)
			continue
		}
		published = append(published, e.ID)
	}
	if err := done(ctx, published, failed); err != nil {
		return 0, err
	}
	return len(published), nil
}

// Run polls until ctx is cancelled. A full batch triggers an immediate
// follow-up so a backlog drains quickly.
func (r *Relay) Run(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		n, err := r.RunOnce(ctx)
		switch {
		case errors.Is(err, context.Canceled):
			return nil
		case err != nil:
			r.logger().Error("relay batch failed", "err", err)
		case n > 0:
			r.logger().Info("published events", "count", n)
		}
		next := interval
		if err == nil && n == r.batchSize() {
			next = 0
		}
		timer.Reset(next)
	}
}

func (r *Relay) batchSize() int {
	if r.BatchSize <= 0 {
		return 100
	}
	return r.BatchSize
}

func (r *Relay) logger() *slog.Logger {
	if r.Logger == nil {
		return slog.Default()
	}
	return r.Logger
}
