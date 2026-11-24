CREATE TABLE accounts (
    id             UUID PRIMARY KEY,
    name           TEXT        NOT NULL,
    type           TEXT        NOT NULL CHECK (type IN ('asset','liability','equity','revenue','expense')),
    currency       CHAR(3)     NOT NULL,
    allow_negative BOOLEAN     NOT NULL DEFAULT FALSE,
    balance        BIGINT      NOT NULL DEFAULT 0,
    version        BIGINT      NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL,
    -- Defence in depth: the database itself refuses overdrafts.
    CONSTRAINT balance_non_negative CHECK (allow_negative OR balance >= 0)
);

CREATE TABLE transactions (
    id              UUID PRIMARY KEY,
    idempotency_key TEXT        NOT NULL UNIQUE,
    request_hash    TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    currency        CHAR(3)     NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE postings (
    id             BIGSERIAL PRIMARY KEY,
    transaction_id UUID        NOT NULL REFERENCES transactions(id),
    account_id     UUID        NOT NULL REFERENCES accounts(id),
    direction      TEXT        NOT NULL CHECK (direction IN ('debit','credit')),
    amount         BIGINT      NOT NULL CHECK (amount > 0),
    balance_after  BIGINT      NOT NULL,
    position       SMALLINT    NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (transaction_id, position)
);
CREATE INDEX postings_account_idx ON postings (account_id, id DESC);

-- Transactional outbox: events are written in the same database transaction
-- as the postings and published asynchronously by the relay.
CREATE TABLE outbox (
    id           BIGSERIAL PRIMARY KEY,
    aggregate_id UUID        NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts     INT         NOT NULL DEFAULT 0
);
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
