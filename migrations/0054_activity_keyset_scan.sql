-- Keyset scans allow a failed first batch to retain its events without
-- starving later pending events. C collation matches Go bytewise key order.
CREATE INDEX np_outbox_pending_key_scan
ON np_outbox (topic, event_key COLLATE "C")
WHERE processed_at IS NULL;
