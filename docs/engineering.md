# Engineering design

Architecture, layout, schema and API are in the [README](../README.md) and the
[ADRs](adr). This document covers the cross-cutting concerns.

## Service boundaries

| Component | Owns | Talks to |
|---|---|---|
| `ledgerd` | Accounts, transactions, postings, idempotency records, outbox rows | Postgres |
| `relay` | Nothing; it moves outbox rows | Postgres, NATS JetStream |
| Consumers (outside this repo) | Their own read models | NATS |

The ledger is the only writer of its tables. Other services never read the
database directly; they call the API or consume events. This keeps the schema
free to change.

## Authentication and authorisation

ledgerflow is an internal service and does not authenticate end users.

- **Current:** deployed behind the platform ingress with network policy limiting
  callers to the payments namespace (see `infra-blueprint`).
- **Planned (M4):** mTLS service identity at the mesh, plus a per-caller allow-list
  of account types each caller may debit. A payments service may debit
  `cash-at-psp`; it may not debit another customer's wallet.

## Audit logging

The postings table *is* the audit log: append-only, every row tied to a
transaction with a timestamp, idempotency key, request hash and description. Planned
(M4): an `actor` column populated from the caller's service identity, and a
database role for `ledgerd` that has `INSERT` but no `UPDATE`/`DELETE` on
`postings` and `transactions`.

## Security design

| Threat | Mitigation |
|---|---|
| Double spend through concurrent requests | Row locks in ID order; non-negative `CHECK` constraint as a backstop |
| Replay of a captured request | Idempotency key + request hash; a different body under the same key is rejected |
| Tampering with history | No update or delete paths in the code; DB role hardening planned (M4) |
| SQL injection | All queries parameterised through pgx |
| Oversized or slow requests | Server read/write timeouts; request body size limit planned (M3) |
| Supply chain | Go modules pinned in `go.sum`, Dependabot, distroless non-root image |

## Performance and scaling

- Writes scale vertically on the Postgres primary. Hot accounts (a single
  platform fee account credited by every transaction) are the first bottleneck
  because every posting locks that row. The plan is sharded sub-accounts that
  roll up, tracked in M5.
- Reads (`GET` balance) are a primary-key lookup. History reads use the
  `(account_id, id DESC)` index and are bounded by `limit`.
- The relay scales horizontally: `SKIP LOCKED` lets several relays claim
  disjoint batches.

## Observability

- **Logs:** JSON via `slog`, one line per request with route, status and latency.
- **Metrics:** `ledgerflow_http_requests_total{route,status}` and
  `ledgerflow_http_request_duration_seconds{route}`. Planned: outbox lag and relay
  publish failures.
- **SLOs:** 99.9% of `POST /v1/transactions` succeed (non-5xx) and 99% complete
  under 100 ms, over 28 days. Burn-rate alerts are defined in
  `infra-blueprint/k8s/platform/slo-alerts.yaml`.

## Disaster recovery

| | Target | How |
|---|---|---|
| RPO | 5 minutes | Postgres continuous WAL archiving with point-in-time restore (RDS, see infra-blueprint) |
| RTO | 1 hour | Restore to a new instance, repoint `LEDGER_DATABASE_URL`, redeploy |
| Outbox after restore | No lost events | Unpublished rows survive in the restored DB; consumers must be idempotent on transaction ID because some events will be re-sent |

Restore follows the [PostgreSQL point-in-time restore runbook](https://github.com/jakhongirjp/infra-blueprint/blob/main/docs/runbooks/postgres-point-in-time-restore.md).

## DevOps

- **CI** (`.github/workflows/ci.yml`): `golangci-lint`, unit tests with the race
  detector, integration tests against a Postgres service container, Docker build.
- **CD:** the image is deployed by Argo CD from `infra-blueprint`
  (`k8s/argocd/apps/*/ledgerd.yaml`). Staging follows `main`; production is
  promoted by changing the image tag in Git.
- **Migrations** run at startup under a Postgres advisory lock, so concurrent
  pods don't race. Migrations must be backwards compatible with the previous
  release (expand, then contract in a later release).

## Testing strategy

| Layer | What | Where |
|---|---|---|
| Domain | Balance rules, validation, normal sides | `internal/domain/*_test.go` |
| Store contract | One suite run against both the memory and Postgres stores, including concurrent overdraft attempts | `internal/store/storetest` |
| HTTP | Status codes, problem types, idempotency replay, metrics | `internal/httpapi/server_test.go` |
| Outbox | Batching, failure handling | `internal/outbox/relay_test.go` |
| Planned | Property-based tests (sum of all balances is zero), k6 load test | M3, M5 |

## Documentation strategy

- README: what it is, how to run it, the API.
- ADRs for decisions that would be expensive to reverse.
- This document for cross-cutting concerns; `product.md` for the why.
- Runbooks live with the platform in `infra-blueprint`.
- Docs change in the same PR as the behaviour they describe.
