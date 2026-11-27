-- Shared host authority for participating PostgreSQL instances. Existing
-- local grants are deliberately not promoted into shared authorization.
CREATE TABLE np_plugin_task_authorizations (
    slug text PRIMARY KEY,
    revision bigint NOT NULL CHECK (revision > 0),
    epoch text NOT NULL,
    enabled boolean NOT NULL,
    retired boolean NOT NULL,
    data bytea NOT NULL
);
