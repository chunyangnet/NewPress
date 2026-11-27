CREATE TABLE IF NOT EXISTS np_questions_state (
 id integer PRIMARY KEY CHECK (id = 1),
 data jsonb NOT NULL
);
INSERT INTO np_questions_state (id, data) VALUES (1, '{"schema":1,"next_id":1,"items":[],"receipts":[],"events":[]}') ON CONFLICT DO NOTHING;
