# ADR-0001: Pessimistic row locks over optimistic retries

- Status: accepted
- Date: 2026-11-24

## Context

Ledger writes concentrate on a few hot accounts (a platform's settlement or
fee account takes part in most transactions). We need correctness under
concurrency without forcing clients to retry.

## Options

1. **Optimistic concurrency** with a `version` column and `UPDATE … WHERE
   version = $n`. Cheap when contention is low, but hot accounts turn into
   retry storms and tail latency grows with load.
2. **SERIALIZABLE isolation** and retry on serialization failure. Correct, but
   pushes retry logic into every code path and aborts are hard to reason about.
3. **`SELECT … FOR UPDATE` on the referenced accounts, in primary-key order.**

## Decision

Option 3. Locks are taken in a single statement ordered by `id`, so any two
transactions over overlapping accounts acquire locks in the same order and
cannot deadlock. The `version` column is still incremented so that readers and
event consumers can detect ordering.

## Consequences

- Throughput on a single hot account is bounded by one transaction at a time.
  This is the correct semantics for a balance, and the critical section is
  short (one round trip of batched statements).
- If one account becomes a throughput bottleneck we can shard it into
  sub-accounts and sum them, without changing this decision.
- A `CHECK (allow_negative OR balance >= 0)` constraint gives a database-level
  guarantee even if application code regresses.
