-- Rebuildable reference counts; canonical source and editorial versions remain
-- authoritative. No foreign key to snippets: articles may name missing IDs.
ALTER TABLE np_posts ADD COLUMN snippet_refs_ready boolean NOT NULL DEFAULT false;
CREATE INDEX np_posts_snippet_refs_missing ON np_posts(id) WHERE NOT snippet_refs_ready;
CREATE TABLE np_post_snippet_refs (
    post_id bigint NOT NULL REFERENCES np_posts(id) ON DELETE CASCADE,
    snippet_id bigint NOT NULL CHECK (snippet_id > 0),
    body_version bigint NOT NULL CHECK (body_version > 0),
    occurrences integer NOT NULL CHECK (occurrences > 0),
    PRIMARY KEY (post_id,snippet_id,body_version)
);
CREATE INDEX np_post_snippet_refs_lookup ON np_post_snippet_refs(snippet_id,post_id,body_version) INCLUDE (occurrences);

CREATE FUNCTION np_invalidate_snippet_refs() RETURNS trigger LANGUAGE plpgsql AS $invalidate$
BEGIN
    NEW.snippet_refs_ready := false;
    RETURN NEW;
END
$invalidate$;
CREATE TRIGGER np_posts_invalidate_snippet_refs BEFORE UPDATE ON np_posts FOR EACH ROW
WHEN (OLD.content_source IS DISTINCT FROM NEW.content_source)
EXECUTE FUNCTION np_invalidate_snippet_refs();
