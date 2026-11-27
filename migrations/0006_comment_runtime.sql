ALTER TABLE np_comments
    ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS author_ip_hash char(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS user_agent varchar(512) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS moderated_by varchar(160) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS moderated_at timestamptz;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_comments'::regclass AND conname = 'np_comments_status_valid') THEN
        ALTER TABLE np_comments
            ADD CONSTRAINT np_comments_status_valid CHECK (status IN ('pending', 'approved', 'spam', 'trash'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_comments'::regclass AND conname = 'np_comments_version_positive') THEN
        ALTER TABLE np_comments
            ADD CONSTRAINT np_comments_version_positive CHECK (version > 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_comments'::regclass AND conname = 'np_comments_content_bounded') THEN
        ALTER TABLE np_comments
            ADD CONSTRAINT np_comments_content_bounded CHECK (
                octet_length(content_source) BETWEEN 1 AND 12288
                AND octet_length(content_html) BETWEEN 1 AND 65536
            );
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_comments'::regclass AND conname = 'np_comments_ip_hash_valid') THEN
        ALTER TABLE np_comments
            ADD CONSTRAINT np_comments_ip_hash_valid CHECK (
                author_ip_hash = '' OR author_ip_hash ~ '^[0-9a-f]{64}$'
            );
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_comments'::regclass AND conname = 'np_comments_parent_not_self') THEN
        ALTER TABLE np_comments
            ADD CONSTRAINT np_comments_parent_not_self CHECK (parent_id IS NULL OR parent_id <> id);
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS np_comments_status_updated_idx
    ON np_comments (status, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS np_comments_post_parent_created_idx
    ON np_comments (post_id, parent_id, created_at, id);

