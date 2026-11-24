# CTO review checklist

An honest scorecard of where the project stands. Unchecked items have an issue.

## Architecture
- [x] Domain logic has no I/O and is tested without a database
- [x] Storage behind an interface with a shared contract test suite
- [x] Decisions recorded as ADRs
- [x] Clear ownership: only `ledgerd` writes ledger tables
- [ ] Event schema versioned and published (M9)

## Correctness
- [x] Double-entry invariant enforced in code and in the schema
- [x] Concurrency tested: parallel overdraft attempts against Postgres
- [x] Idempotency with request-hash conflict detection
- [ ] Property-based test that all balances sum to zero (M3)
- [ ] Reversal transactions (M3)

## Security
- [x] Parameterised SQL only
- [x] Non-root distroless image
- [x] Dependency updates automated
- [ ] Service-to-service authentication (M4)
- [ ] Append-only database role (M4)
- [ ] Request body size limit (M3)

## Operations
- [x] Health, readiness and metrics endpoints
- [x] Graceful shutdown with timeout
- [x] Migrations safe under concurrent startup
- [x] DR targets stated with a restore runbook
- [ ] Distributed tracing (M8)
- [ ] Outbox lag alert (M8)
- [ ] Load test baseline (M5)

## Delivery
- [x] CI on every PR: lint, unit, integration, image build
- [x] Conventional commits, PR template, CODEOWNERS
- [x] GitOps deployment defined in infra-blueprint
- [ ] Signed release artifacts and SBOM
