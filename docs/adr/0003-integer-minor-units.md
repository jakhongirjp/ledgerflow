# ADR-0003: Integer minor units for money

- Status: accepted
- Date: 2026-11-24

## Decision

Amounts are `int64` minor units (cents for USD) end to end: JSON, Go and
`BIGINT` columns. Floating point is never used. Additions are overflow-checked.

## Consequences

- No rounding errors and exact equality checks for debits vs credits.
- Clients must know the currency's exponent (2 for USD, 0 for JPY). A currency
  metadata endpoint is on the roadmap.
