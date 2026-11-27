-- Canonical rows stay in place. Unresolved evidence blocks current-version
-- snippet/editorial operations until an explicit, validated offline repair.
CREATE TABLE np_snippet_quarantines (
    id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{64}$'),
    scope text NOT NULL CHECK (scope IN ('snippet','editorial_receipt')),
    reference text NOT NULL CHECK (reference ~ '^[0-9a-f]{64}$'),
    captured_at timestamptz NOT NULL,
    repaired boolean NOT NULL DEFAULT false,
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 67108864),
    CHECK ((id = convert_from(data,'UTF8')::jsonb->>'id') IS TRUE),
    CHECK ((reference = convert_from(data,'UTF8')::jsonb#>>'{capture,reference}') IS TRUE),
    CHECK ((captured_at = (convert_from(data,'UTF8')::jsonb->>'captured_at')::timestamptz) IS TRUE),
    CHECK (scope = CASE WHEN (convert_from(data,'UTF8')::jsonb#>>'{capture,target,snippet_id}')::bigint > 0
                       THEN 'snippet' ELSE 'editorial_receipt' END),
    CHECK (repaired = (convert_from(data,'UTF8')::jsonb ? 'repair')),
    CHECK ((NOT repaired OR (jsonb_typeof(convert_from(data,'UTF8')::jsonb->'repair') = 'object'
        AND convert_from(data,'UTF8')::jsonb#>>'{repair,replacement_sha256}' ~ '^[0-9a-f]{64}$'
        AND convert_from(data,'UTF8')::jsonb#>>'{repair,result_reference}' ~ '^[0-9a-f]{64}$')) IS TRUE)
);
CREATE INDEX np_snippet_quarantines_unresolved ON np_snippet_quarantines (id) WHERE NOT repaired;

CREATE FUNCTION np_protect_snippet_quarantine() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'UPDATE' THEN
        RAISE EXCEPTION 'snippet quarantine evidence is permanent';
    END IF;
    IF OLD.repaired OR NOT NEW.repaired OR NEW.id <> OLD.id OR NEW.scope <> OLD.scope
       OR NEW.reference <> OLD.reference OR NEW.captured_at <> OLD.captured_at
       OR (convert_from(NEW.data,'UTF8')::jsonb - 'repair') IS DISTINCT FROM
          (convert_from(OLD.data,'UTF8')::jsonb - 'repair') THEN
        RAISE EXCEPTION 'only a first repair audit may be appended to snippet quarantine evidence';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER np_snippet_quarantine_immutable BEFORE UPDATE OR DELETE ON np_snippet_quarantines
    FOR EACH ROW EXECUTE FUNCTION np_protect_snippet_quarantine();
CREATE TRIGGER np_snippet_quarantine_no_truncate BEFORE TRUNCATE ON np_snippet_quarantines
    FOR EACH STATEMENT EXECUTE FUNCTION np_protect_snippet_quarantine();
