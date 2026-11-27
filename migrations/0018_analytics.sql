-- Bounded aggregate document, updated by one idempotent batch transaction.
-- No request identifiers or raw visitor data enter this table.
CREATE TABLE np_analytics (
    id smallint PRIMARY KEY CHECK (id = 1),
    data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO np_analytics(id,data) VALUES (1,'{"schema":1,"days":{},"writers":{},"updated_at":"0001-01-01T00:00:00Z"}');
