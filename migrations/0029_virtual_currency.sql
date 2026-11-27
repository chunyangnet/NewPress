CREATE TABLE np_currency_settings (
    id smallint PRIMARY KEY CHECK (id = 1),
    version bigint NOT NULL CHECK (version > 0),
    enabled boolean NOT NULL DEFAULT false,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40),
    symbol text NOT NULL CHECK (char_length(symbol) BETWEEN 1 AND 12),
    precision smallint NOT NULL CHECK (precision BETWEEN 0 AND 6),
    max_balance bigint NOT NULL CHECK (max_balance > 0),
    allow_transfers boolean NOT NULL DEFAULT false,
    min_transfer bigint NOT NULL CHECK (min_transfer > 0),
    max_transfer bigint NOT NULL CHECK (max_transfer >= min_transfer),
    daily_transfer_limit bigint NOT NULL CHECK (daily_transfer_limit >= max_transfer),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO np_currency_settings(id, version, enabled, name, symbol, precision, max_balance,
    allow_transfers, min_transfer, max_transfer, daily_transfer_limit)
VALUES (1, 1, false, '积分', '积分', 1, 10000000000, false, 1, 1000000, 2000000)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE np_currency_balances (
    user_id bigint PRIMARY KEY REFERENCES np_users(id) ON DELETE CASCADE,
    balance bigint NOT NULL DEFAULT 0 CHECK (balance >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE np_currency_operations (
    id bigserial PRIMARY KEY,
    actor_id bigint NOT NULL CHECK (actor_id >= 0),
    request_id text NOT NULL CHECK (char_length(request_id) BETWEEN 16 AND 80),
    fingerprint char(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(actor_id, request_id)
);

CREATE TABLE np_currency_entries (
    id bigserial PRIMARY KEY,
    operation_id bigint NOT NULL REFERENCES np_currency_operations(id),
    user_id bigint NOT NULL REFERENCES np_users(id) ON DELETE CASCADE,
    peer_id bigint REFERENCES np_users(id) ON DELETE SET NULL,
    actor_id bigint NOT NULL CHECK (actor_id >= 0),
    delta bigint NOT NULL CHECK (delta <> 0),
    balance bigint NOT NULL CHECK (balance >= 0),
    kind text NOT NULL CHECK (kind IN ('admin_credit','admin_debit','credit','transfer_in','transfer_out')),
    reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 200),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX np_currency_entries_user_history ON np_currency_entries(user_id, id DESC);
CREATE INDEX np_currency_entries_user_transfers ON np_currency_entries(user_id, created_at DESC) WHERE kind = 'transfer_out';

COMMENT ON TABLE np_currency_entries IS 'Append-only virtual currency ledger; balances are maintained transactionally with entries.';
