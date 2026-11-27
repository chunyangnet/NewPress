-- Terminal task history releases private payloads while retaining permanent
-- enqueue deduplication. Shared event history retains the frozen cohort and its
-- delivered/cancelled facts. Application transactions move records atomically.
CREATE TABLE np_plugin_job_archives (
    id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{64}$'),
    plugin text NOT NULL,
    status text NOT NULL CHECK (status IN ('succeeded','cancelled')),
    updated_at timestamptz NOT NULL,
    archived_at timestamptz NOT NULL CHECK (archived_at >= updated_at),
    data bytea NOT NULL CHECK (octet_length(data) <= 4096)
);
CREATE INDEX np_plugin_job_archives_plugin ON np_plugin_job_archives (plugin,updated_at DESC,id);

CREATE TABLE np_plugin_fanout_archives (
    event_key text PRIMARY KEY,
    archived_at timestamptz NOT NULL,
    cancelled boolean NOT NULL,
    data bytea NOT NULL CHECK (octet_length(data) <= 524288)
);
