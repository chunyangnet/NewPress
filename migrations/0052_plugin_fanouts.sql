CREATE TABLE np_plugin_fanouts (
    event_key text PRIMARY KEY,
    data bytea NOT NULL CHECK (octet_length(data) <= 524288)
);
