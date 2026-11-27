CREATE TABLE IF NOT EXISTS np_sessions (
    id varchar(43) PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9_-]{43}$'),
    user_id bigint REFERENCES np_users(id) ON DELETE CASCADE,
    identity_key varchar(330) NOT NULL,
    username varchar(320) NOT NULL,
    display_name varchar(1024) NOT NULL,
    role_name varchar(1024) NOT NULL,
    credential_digest varchar(43) NOT NULL CHECK (credential_digest ~ '^[A-Za-z0-9_-]{43}$'),
    csrf_token varchar(43) NOT NULL CHECK (csrf_token ~ '^[A-Za-z0-9_-]{43}$'),
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    ip_address varchar(64) NOT NULL DEFAULT '',
    user_agent varchar(512) NOT NULL DEFAULT '',
    CHECK (last_seen_at >= created_at AND expires_at > last_seen_at),
    CHECK (identity_key = CASE WHEN user_id IS NULL THEN 'name:' || lower(username) ELSE 'id:' || user_id::text END)
);

CREATE INDEX IF NOT EXISTS np_sessions_identity_seen_idx ON np_sessions (identity_key, last_seen_at, id);
CREATE INDEX IF NOT EXISTS np_sessions_expires_idx ON np_sessions (expires_at);
CREATE INDEX IF NOT EXISTS np_sessions_last_seen_idx ON np_sessions (last_seen_at, id);
