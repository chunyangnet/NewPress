ALTER TABLE np_comments
    ADD COLUMN IF NOT EXISTS staff_reply boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS created_by varchar(160) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS edited_by varchar(160) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS edited_at timestamptz;

CREATE TABLE IF NOT EXISTS np_comment_revisions (
    comment_id bigint NOT NULL REFERENCES np_comments(id) ON DELETE CASCADE,
    version bigint NOT NULL,
    author_name varchar(160) NOT NULL,
    author_email varchar(320) NOT NULL,
    status varchar(24) NOT NULL,
    content_source text NOT NULL,
    content_html text NOT NULL,
    actor_label varchar(160) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (comment_id, version),
    CONSTRAINT np_comment_revisions_version_positive CHECK (version > 0),
    CONSTRAINT np_comment_revisions_status_valid CHECK (status IN ('pending', 'approved', 'spam', 'trash')),
    CONSTRAINT np_comment_revisions_content_bounded CHECK (
        octet_length(content_source) BETWEEN 1 AND 12288
        AND octet_length(content_html) BETWEEN 1 AND 65536
    )
);

CREATE INDEX IF NOT EXISTS np_comment_revisions_comment_created_idx
    ON np_comment_revisions (comment_id, created_at DESC, version DESC);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_comments'::regclass AND conname = 'np_comments_edit_metadata_valid') THEN
        ALTER TABLE np_comments
            ADD CONSTRAINT np_comments_edit_metadata_valid CHECK (
                (edited_at IS NULL AND edited_by = '') OR
                (edited_at IS NOT NULL AND edited_by <> '')
            );
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_comments'::regclass AND conname = 'np_comments_staff_reply_metadata_valid') THEN
        ALTER TABLE np_comments
            ADD CONSTRAINT np_comments_staff_reply_metadata_valid CHECK (
                NOT staff_reply OR created_by <> ''
            );
    END IF;
END
$$;
