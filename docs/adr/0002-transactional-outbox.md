# ADR-0002: Transactional outbox for event delivery

- Status: accepted
- Date: 2026-11-24

## Context

Other services (notifications, analytics, risk) must react to every posting.
Publishing to a broker directly from the request path creates a dual-write
problem: the DB commit can succeed while the publish fails, or the reverse.

## Decision

Write an `outbox` row in the same database transaction as the postings. A
separate `relay` process claims unpublished rows with `FOR UPDATE SKIP LOCKED`,
publishes them to NATS JetStream, waits for the ack, and marks them published.

## Consequences

- Delivery is **at-least-once**. A crash after the ack and before the commit
  resends the batch. The outbox ID is sent as `Nats-Msg-Id`, so JetStream drops
  duplicates inside its window, and consumers should still be idempotent.
- Ordering is per relay batch; consumers that need strict per-account order
  should use the account `version` carried in the payload.
- Relay replicas scale horizontally without coordination.
- The outbox table needs periodic pruning of published rows (a follow-up job).
