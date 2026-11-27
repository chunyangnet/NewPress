ALTER TABLE np_terms
    ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS is_system boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_terms'::regclass AND conname = 'np_terms_version_positive'
    ) THEN
        ALTER TABLE np_terms
            ADD CONSTRAINT np_terms_version_positive CHECK (version > 0);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid = 'np_terms'::regclass AND conname = 'np_terms_parent_not_self'
    ) THEN
        ALTER TABLE np_terms
            ADD CONSTRAINT np_terms_parent_not_self CHECK (parent_id IS NULL OR parent_id <> id);
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS np_terms_taxonomy_name_idx
    ON np_terms (taxonomy_id, lower(name), id);

CREATE INDEX IF NOT EXISTS np_terms_parent_idx
    ON np_terms (parent_id, id)
    WHERE parent_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS np_post_terms_term_post_idx
    ON np_post_terms (term_id, post_id);

INSERT INTO np_taxonomies(slug, name, object_types, hierarchical)
VALUES
    ('category', '分类目录', '["post"]'::jsonb, true),
    ('post_tag', '文章标签', '["post"]'::jsonb, false)
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    object_types = EXCLUDED.object_types,
    hierarchical = EXCLUDED.hierarchical;

INSERT INTO np_terms(taxonomy_id, slug, name, description, is_system, version)
SELECT id, 'uncategorized', '未分类', '没有显式分类的文章会安全归入这里。', true, 1
FROM np_taxonomies
WHERE slug = 'category'
ON CONFLICT (taxonomy_id, slug) DO UPDATE SET
    is_system = true,
    version = GREATEST(np_terms.version, 1),
    updated_at = now();
