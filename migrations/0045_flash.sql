-- Flash items reuse canonical content identity, revisions and discussion records.
ALTER TABLE np_posts DROP CONSTRAINT np_posts_type_valid;
ALTER TABLE np_posts ADD CONSTRAINT np_posts_type_valid CHECK (type IN ('post', 'page', 'flash'));
