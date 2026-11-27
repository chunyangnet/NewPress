CREATE TABLE np_theme_settings (
    theme_slug text PRIMARY KEY CHECK (theme_slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$'),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    -- Preserve the validated encoding and its size. jsonb expands scientific
    -- notation and adds spaces, which can break the application's read limit.
    document json NOT NULL CHECK (
        json_typeof(document) = 'object'
        AND json_typeof(document->'theme') IS NOT DISTINCT FROM 'string'
        AND document->>'theme' IS NOT DISTINCT FROM theme_slug
        AND json_typeof(document->'schema') IS NOT DISTINCT FROM 'number'
        AND document->>'schema' IS NOT DISTINCT FROM '1'
        AND json_typeof(document->'values') IS NOT DISTINCT FROM 'object'
    )
);
