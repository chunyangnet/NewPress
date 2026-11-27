ALTER TABLE np_posts
    ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1;

CREATE INDEX IF NOT EXISTS np_posts_type_status_updated_idx
    ON np_posts (type, status, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS np_posts_slug_lookup_idx
    ON np_posts (type, slug, status, published_at);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_posts'::regclass AND conname = 'np_posts_version_positive'
    ) THEN
        ALTER TABLE np_posts
            ADD CONSTRAINT np_posts_version_positive CHECK (version > 0);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_posts'::regclass AND conname = 'np_posts_type_valid'
    ) THEN
        ALTER TABLE np_posts
            ADD CONSTRAINT np_posts_type_valid CHECK (type IN ('post', 'page'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_posts'::regclass AND conname = 'np_posts_status_valid'
    ) THEN
        ALTER TABLE np_posts
            ADD CONSTRAINT np_posts_status_valid
            CHECK (status IN ('draft', 'pending', 'publish', 'future', 'private', 'trash'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_posts'::regclass AND conname = 'np_posts_comment_status_valid'
    ) THEN
        ALTER TABLE np_posts
            ADD CONSTRAINT np_posts_comment_status_valid CHECK (comment_status IN ('open', 'closed'));
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS np_post_revisions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id bigint NOT NULL REFERENCES np_posts(id) ON DELETE CASCADE,
    post_version bigint NOT NULL,
    snapshot jsonb NOT NULL,
    actor varchar(160) NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (post_id, post_version)
);

CREATE INDEX IF NOT EXISTS np_post_revisions_post_created_idx
    ON np_post_revisions (post_id, created_at DESC, id DESC);
