-- Keep minimal creation receipts after a comment or its article is deleted.
-- No expiry or cascading foreign key may turn a retry into another creation.
CREATE TABLE np_comment_submissions (
    key_hash text PRIMARY KEY CHECK (key_hash ~ '^[0-9a-f]{64}$'),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    comment_id bigint NOT NULL UNIQUE CHECK (comment_id > 0),
    post_id bigint NOT NULL CHECK (post_id > 0),
    created_at timestamptz NOT NULL
);
