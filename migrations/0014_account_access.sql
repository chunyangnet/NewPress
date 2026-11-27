ALTER TABLE np_users ADD COLUMN email_verified_at timestamptz;

CREATE TABLE np_account_access (
    token_hash text PRIMARY KEY CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    kind text NOT NULL CHECK (kind IN ('register','recover','invite')),
    email text NOT NULL CHECK (length(email) BETWEEN 3 AND 320),
    user_id bigint NOT NULL DEFAULT 0 CHECK (user_id >= 0),
    user_version bigint NOT NULL DEFAULT 0 CHECK (user_version >= 0),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    consumed_at timestamptz,
    window_at timestamptz NOT NULL,
    requests integer NOT NULL CHECK (requests BETWEEN 1 AND 5),
    UNIQUE(email,kind)
);
CREATE INDEX np_account_access_expiry ON np_account_access(expires_at);
