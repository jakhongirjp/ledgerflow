package outbox

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetStreamPublisher publishes to NATS JetStream and waits for the ack.
// The outbox ID is sent as Nats-Msg-Id so JetStream drops duplicates that
// arrive inside the stream's dedup window.
type JetStreamPublisher struct {
	JS            jetstream.JetStream
	SubjectPrefix string // e.g. "ledger.events"
}

func (p JetStreamPublisher) Publish(ctx context.Context, e Event) error {
	msg := nats.NewMsg(p.SubjectPrefix + "." + e.Type)
	msg.Data = e.Payload
	msg.Header.Set(nats.MsgIdHdr, strconv.FormatInt(e.ID, 10))
	msg.Header.Set("Aggregate-Id", e.AggregateID)
	_, err := p.JS.PublishMsg(ctx, msg)
	return err
}

// LogPublisher writes events to the log. Useful for local development.
type LogPublisher struct{ Logger *slog.Logger }

func (p LogPublisher) Publish(_ context.Context, e Event) error {
	p.Logger.Info("event", "id", e.ID, "type", e.Type, "aggregate_id", e.AggregateID, "payload", string(e.Payload))
	return nil
}
