-- Permanent internal-write deduplication. No cascading foreign keys: task
-- archive, content deletion and account changes must not erase old receipts.
CREATE TABLE np_plugin_content_effects (
    id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{64}$'),
    job_id text NOT NULL CHECK (job_id ~ '^[a-f0-9]{64}$'),
    slot text NOT NULL CHECK (slot ~ '^[A-Za-z][A-Za-z0-9_.-]{0,63}$'),
    data bytea NOT NULL CHECK (octet_length(data) <= 2048),
    UNIQUE(job_id, slot)
);
