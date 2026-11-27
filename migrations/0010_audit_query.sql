CREATE INDEX IF NOT EXISTS np_audit_events_action_created_idx
    ON np_audit_events (action, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS np_audit_events_type_created_idx
    ON np_audit_events (object_type, created_at DESC, id DESC);
