CREATE TABLE IF NOT EXISTS newpress_schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS np_users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username varchar(80) NOT NULL UNIQUE,
    email varchar(320) NOT NULL UNIQUE,
    password_hash text NOT NULL,
    display_name varchar(160) NOT NULL DEFAULT '',
    status varchar(24) NOT NULL DEFAULT 'active',
    locale varchar(20) NOT NULL DEFAULT 'zh-CN',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS np_roles (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug varchar(80) NOT NULL UNIQUE,
    name varchar(120) NOT NULL,
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE TABLE IF NOT EXISTS np_user_roles (
    user_id bigint NOT NULL REFERENCES np_users(id) ON DELETE CASCADE,
    role_id bigint NOT NULL REFERENCES np_roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE IF NOT EXISTS np_posts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id bigint REFERENCES np_users(id) ON DELETE SET NULL,
    type varchar(40) NOT NULL DEFAULT 'post',
    status varchar(24) NOT NULL DEFAULT 'draft',
    slug varchar(240) NOT NULL,
    title text NOT NULL,
    excerpt text NOT NULL DEFAULT '',
    content_source text NOT NULL DEFAULT '',
    content_html text NOT NULL DEFAULT '',
    cover_url text NOT NULL DEFAULT '',
    comment_status varchar(24) NOT NULL DEFAULT 'open',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(excerpt, '') || ' ' || coalesce(content_source, ''))
    ) STORED,
    UNIQUE (type, slug)
);

CREATE INDEX IF NOT EXISTS np_posts_status_published_idx ON np_posts (status, published_at DESC);
CREATE INDEX IF NOT EXISTS np_posts_search_idx ON np_posts USING gin (search_vector);

CREATE TABLE IF NOT EXISTS np_taxonomies (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug varchar(80) NOT NULL UNIQUE,
    name varchar(120) NOT NULL,
    object_types jsonb NOT NULL DEFAULT '["post"]'::jsonb,
    hierarchical boolean NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS np_terms (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    taxonomy_id bigint NOT NULL REFERENCES np_taxonomies(id) ON DELETE CASCADE,
    parent_id bigint REFERENCES np_terms(id) ON DELETE SET NULL,
    slug varchar(160) NOT NULL,
    name varchar(160) NOT NULL,
    description text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (taxonomy_id, slug)
);

CREATE TABLE IF NOT EXISTS np_post_terms (
    post_id bigint NOT NULL REFERENCES np_posts(id) ON DELETE CASCADE,
    term_id bigint NOT NULL REFERENCES np_terms(id) ON DELETE CASCADE,
    position integer NOT NULL DEFAULT 0,
    PRIMARY KEY (post_id, term_id)
);

CREATE TABLE IF NOT EXISTS np_comments (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id bigint NOT NULL REFERENCES np_posts(id) ON DELETE CASCADE,
    parent_id bigint REFERENCES np_comments(id) ON DELETE CASCADE,
    user_id bigint REFERENCES np_users(id) ON DELETE SET NULL,
    author_name varchar(160) NOT NULL DEFAULT '',
    author_email varchar(320) NOT NULL DEFAULT '',
    status varchar(24) NOT NULL DEFAULT 'pending',
    content_source text NOT NULL,
    content_html text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS np_comments_post_status_idx ON np_comments (post_id, status, created_at);

CREATE TABLE IF NOT EXISTS np_media (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id bigint REFERENCES np_users(id) ON DELETE SET NULL,
    storage varchar(40) NOT NULL DEFAULT 'local',
    object_key text NOT NULL UNIQUE,
    public_url text NOT NULL,
    filename text NOT NULL,
    mime_type varchar(160) NOT NULL,
    size_bytes bigint NOT NULL,
    width integer,
    height integer,
    checksum_sha256 char(64) NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS np_options (
    namespace varchar(120) NOT NULL,
    key varchar(160) NOT NULL,
    value jsonb NOT NULL,
    autoload boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, key)
);

CREATE TABLE IF NOT EXISTS np_packages (
    kind varchar(20) NOT NULL,
    slug varchar(120) NOT NULL,
    version varchar(40) NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    manifest jsonb NOT NULL,
    checksum_sha256 char(64) NOT NULL DEFAULT '',
    installed_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, slug)
);

CREATE TABLE IF NOT EXISTS np_plugin_kv (
    plugin_slug varchar(120) NOT NULL,
    key varchar(240) NOT NULL,
    value jsonb NOT NULL,
    expires_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_slug, key)
);

CREATE TABLE IF NOT EXISTS np_outbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic varchar(160) NOT NULL,
    payload jsonb NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 0,
    processed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS np_outbox_pending_idx ON np_outbox (available_at, id) WHERE processed_at IS NULL;

INSERT INTO np_roles (slug, name, capabilities)
VALUES
    ('administrator', '管理员', '["*"]'::jsonb),
    ('editor', '编辑', '["content:read","content:write","media:write","comments:moderate"]'::jsonb),
    ('author', '作者', '["content:read","content:write:own","media:write:own"]'::jsonb),
    ('subscriber', '订阅者', '["content:read","profile:write:own"]'::jsonb)
ON CONFLICT (slug) DO NOTHING;

