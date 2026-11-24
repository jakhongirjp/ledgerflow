# Roadmap

Milestones are outcomes, not dates. Each one is shippable on its own. Open
work for each is tracked as [GitHub issues](https://github.com/jakhongirjp/ledgerflow/issues)
under the matching milestone.

| # | Milestone | Outcome | Status |
|---|---|---|---|
| M1 | Core ledger | Accounts, balanced transactions, postings, non-negative accounts | Done (v0.1) |
| M2 | Safe retries and events | Idempotency keys, transactional outbox, NATS relay | Done (v0.1) |
| M3 | Correctness hardening | Property-based invariant tests, reversal transactions, request size limits | Planned |
| M4 | Security and audit | Service identity, per-caller permissions, actor on every transaction, append-only DB role | Planned |
| M5 | Performance | Load test baseline, hot-account sharding, connection pool tuning | Planned |
| M6 | Multi-currency | FX clearing accounts and rate references | Planned |
| M7 | Reporting | Balance snapshots, point-in-time balances, trial balance export | Planned |
| M8 | Observability | OpenTelemetry tracing, outbox lag metrics, dashboards | Planned |
| M9 | APIs for platforms | gRPC transport, OpenAPI spec, client SDK | Planned |
| M10 | 1.0 | Stable API and event schema with a deprecation policy | Planned |

## Release plan

| Version | Contents |
|---|---|
| v0.1 | M1 and M2: what is on `main` today |
| v0.2 | M3, including the request size limit |
| v0.3 | M4 and M8 (you can't run it for real money without them) |
| v0.4 | M5 and M7 |
| v0.5 | M6 and M9 |
| v1.0 | M10: API and event schema frozen; breaking changes only with a major version |
