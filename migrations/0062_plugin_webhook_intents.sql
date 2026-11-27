-- Durable operation identities and uncertain results have no cascading keys.
-- Archival of tasks or removal of accounts/plugins must not erase these facts.
CREATE TABLE np_plugin_webhook_intents (
    id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{64}$'),
    job_id text NOT NULL CHECK (job_id ~ '^[a-f0-9]{64}$'),
    slot text NOT NULL CHECK (slot ~ '^[A-Za-z][A-Za-z0-9_.-]{0,63}$'),
    version bigint NOT NULL CHECK (version BETWEEN 1 AND 4),
    state text NOT NULL CHECK (state IN ('pending','sending','http_confirmed','uncertain','cancelled','reviewed_received','reviewed_not_received')),
    data bytea NOT NULL CHECK (octet_length(data) <= 32768),
    UNIQUE(job_id, slot)
);
