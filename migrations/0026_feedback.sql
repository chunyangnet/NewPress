-- Configurable theme feedback pages; row locks serialize each bounded board.
CREATE TABLE np_feedback_boards (
    slug text PRIMARY KEY CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    version bigint NOT NULL CHECK (version > 0),
    data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now()
);
