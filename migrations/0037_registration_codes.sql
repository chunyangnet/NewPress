CREATE TABLE np_registration_codes (
    id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    code_hash text NOT NULL UNIQUE CHECK (code_hash ~ '^[0-9a-f]{64}$'),
    code text NOT NULL UNIQUE CHECK (code ~ '^[A-Za-z0-9_-]{4,128}$'),
    enabled boolean NOT NULL,
    max_uses bigint NOT NULL CHECK (max_uses BETWEEN 0 AND 1000000),
    used bigint NOT NULL DEFAULT 0 CHECK (used >= 0),
    starts_at timestamptz,
    expires_at timestamptz,
    note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 200),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    last_used_at timestamptz,
    CHECK (max_uses = 0 OR used <= max_uses),
    CHECK (starts_at IS NULL OR expires_at IS NULL OR expires_at > starts_at)
);

CREATE INDEX np_registration_codes_created ON np_registration_codes (created_at DESC, id);
