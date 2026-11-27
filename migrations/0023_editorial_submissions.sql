-- A receipt commits with its content revision, closing the response-loss and
-- process-crash window for the editor's retryable form submissions.
CREATE TABLE np_content_submissions (
    key_hash text PRIMARY KEY CHECK (key_hash ~ '^[0-9a-f]{64}$'),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    post_id bigint NOT NULL REFERENCES np_posts(id) ON DELETE RESTRICT,
    version bigint NOT NULL CHECK (version > 0),
    action text NOT NULL CHECK (action IN ('create','save','trash','restore','publish','restore_revision')),
    created_at timestamptz NOT NULL,
    CHECK (action <> 'create' OR version = 1)
);
CREATE INDEX np_content_submissions_expiry ON np_content_submissions(created_at) WHERE action <> 'create';
CREATE INDEX np_content_submissions_post ON np_content_submissions(post_id);
