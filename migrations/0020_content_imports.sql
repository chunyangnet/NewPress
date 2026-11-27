CREATE TABLE np_content_imports (
    source text NOT NULL CHECK (octet_length(source) BETWEEN 1 AND 80),
    format text NOT NULL CHECK (format IN ('markdown','wordpress')),
    source_key text NOT NULL CHECK (octet_length(source_key) BETWEEN 1 AND 256),
    digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
    post_id bigint NOT NULL REFERENCES np_posts(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (source,format,source_key)
);
CREATE INDEX np_content_imports_post ON np_content_imports(post_id);
