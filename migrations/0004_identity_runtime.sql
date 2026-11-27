ALTER TABLE np_users
    ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS password_changed_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS last_login_at timestamptz;

ALTER TABLE np_roles
    ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS built_in boolean NOT NULL DEFAULT false;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_users'::regclass AND conname = 'np_users_version_positive') THEN
        ALTER TABLE np_users
            ADD CONSTRAINT np_users_version_positive CHECK (version > 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_users'::regclass AND conname = 'np_users_status_valid') THEN
        ALTER TABLE np_users
            ADD CONSTRAINT np_users_status_valid CHECK (status IN ('active', 'suspended', 'system'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_users'::regclass AND conname = 'np_users_username_trimmed') THEN
        ALTER TABLE np_users
            ADD CONSTRAINT np_users_username_trimmed CHECK (username = btrim(username) AND length(username) >= 3);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_users'::regclass AND conname = 'np_users_email_trimmed') THEN
        ALTER TABLE np_users
            ADD CONSTRAINT np_users_email_trimmed CHECK (email = btrim(email) AND position('@' IN email) > 1);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_roles'::regclass AND conname = 'np_roles_capabilities_array') THEN
        ALTER TABLE np_roles
            ADD CONSTRAINT np_roles_capabilities_array CHECK (jsonb_typeof(capabilities) = 'array');
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS np_users_username_lower_uidx ON np_users (lower(username));
CREATE UNIQUE INDEX IF NOT EXISTS np_users_email_lower_uidx ON np_users (lower(email));
CREATE INDEX IF NOT EXISTS np_users_status_updated_idx ON np_users (status, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS np_user_roles_role_user_idx ON np_user_roles (role_id, user_id);

CREATE TABLE IF NOT EXISTS np_audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_user_id bigint REFERENCES np_users(id) ON DELETE SET NULL,
    actor_label varchar(160) NOT NULL DEFAULT '',
    action varchar(160) NOT NULL,
    object_type varchar(80) NOT NULL,
    object_id varchar(160) NOT NULL DEFAULT '',
    request_id varchar(128) NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS np_audit_events_created_idx ON np_audit_events (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS np_audit_events_object_idx ON np_audit_events (object_type, object_id, created_at DESC);

INSERT INTO np_roles(slug, name, description, capabilities, built_in)
VALUES
    ('administrator', '管理员', '拥有完整站点控制权，包括用户、设置、主题、内容和运维能力。', '["*"]'::jsonb, true),
    ('editor', '编辑', '管理全部内容、分类、媒体与评论，但不能修改用户、主题或站点策略。', '["admin:access","comments:moderate","content:read","content:write","dashboard:read","media:read","media:write","system:read","taxonomy:manage"]'::jsonb, true),
    ('author', '作者', '进入写作后台并管理自己的文章；对象级边界由内容仓库和 HTTP 层共同执行。', '["admin:access","content:read","content:write:own","dashboard:read","media:read","profile:write:own"]'::jsonb, true),
    ('subscriber', '订阅者', '仅维护自己的公开资料，不进入管理后台。', '["profile:write:own"]'::jsonb, true)
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    capabilities = EXCLUDED.capabilities,
    built_in = true;

