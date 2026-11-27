-- Private maintenance evidence retains the original encoding and SQL metadata.
-- Admission/maintenance transactions enforce aggregate byte/count budgets and
-- verify all selection columns against data, including repaired records.
CREATE TABLE np_plugin_record_quarantines (
    id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{64}$'),
    record_type text NOT NULL CHECK (record_type IN ('task','task_history','fanout','fanout_history')),
    reference text NOT NULL CHECK (reference ~ '^[a-f0-9]{64}$'),
    captured_at timestamptz NOT NULL,
    repaired boolean NOT NULL,
    data bytea NOT NULL CHECK (octet_length(data) <= 67108864)
);
CREATE INDEX np_plugin_record_quarantines_type ON np_plugin_record_quarantines(record_type,repaired,id);
