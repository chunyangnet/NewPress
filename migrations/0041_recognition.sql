-- Community identity awards, private certification applications and article tip receipts.
CREATE TABLE np_recognition_state (
    id smallint PRIMARY KEY CHECK (id = 1),
    data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object')
);
