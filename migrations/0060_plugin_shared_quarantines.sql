-- Original authority/effect bytes and indexes are retained independently.
-- Unresolved evidence blocks shared work until explicitly repaired.
CREATE TABLE np_plugin_shared_quarantines (
    id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{64}$'),
    record_type text NOT NULL CHECK (record_type IN ('authority','content_effect')),
    reference text NOT NULL CHECK (reference ~ '^[a-f0-9]{64}$'),
    captured_at timestamptz NOT NULL,
    repaired boolean NOT NULL,
    data bytea NOT NULL CHECK (octet_length(data) <= 67108864)
);
CREATE INDEX np_plugin_shared_quarantines_type ON np_plugin_shared_quarantines(record_type,repaired,id);
