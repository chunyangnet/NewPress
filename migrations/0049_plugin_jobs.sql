-- Durable extension tasks. JSON bytes preserve the request fingerprint exactly;
-- indexed columns support bounded row-level claims without loading the queue.
CREATE TABLE np_plugin_jobs (
    id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{64}$'),
    plugin text NOT NULL,
    status text NOT NULL CHECK (status IN ('pending','running','retry','succeeded','failed','cancelled')),
    available_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    lease_until timestamptz,
    data bytea NOT NULL CHECK (octet_length(data) <= 65536)
);
CREATE INDEX np_plugin_jobs_ready ON np_plugin_jobs (available_at,id)
    WHERE status IN ('pending','retry');
CREATE INDEX np_plugin_jobs_expired ON np_plugin_jobs (lease_until,id)
    WHERE status='running';
CREATE INDEX np_plugin_jobs_plugin ON np_plugin_jobs (plugin,updated_at DESC,id);
