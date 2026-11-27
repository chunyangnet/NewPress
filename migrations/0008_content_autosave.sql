CREATE TABLE IF NOT EXISTS np_post_autosaves (
    post_id bigint NOT NULL REFERENCES np_posts(id) ON DELETE CASCADE,
    owner_key varchar(80) NOT NULL,
    source_version bigint NOT NULL,
    snapshot jsonb NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (post_id, owner_key),
    CONSTRAINT np_post_autosaves_owner_key_valid CHECK (
        owner_key ~ '^user:[1-9][0-9]{0,18}$'
        OR owner_key ~ '^legacy:[0-9a-f]{64}$'
    ),
    CONSTRAINT np_post_autosaves_source_version_positive CHECK (source_version > 0),
    CONSTRAINT np_post_autosaves_snapshot_object CHECK (jsonb_typeof(snapshot) = 'object'),
    CONSTRAINT np_post_autosaves_expiry_valid CHECK (expires_at > updated_at)
);

CREATE INDEX IF NOT EXISTS np_post_autosaves_expires_idx
    ON np_post_autosaves (expires_at, post_id);
