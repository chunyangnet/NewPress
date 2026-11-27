CREATE TABLE np_engagement (
    user_id bigint NOT NULL REFERENCES np_users(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('bookmark', 'like', 'follow')),
    target_id bigint NOT NULL CHECK (target_id > 0),
    collection text NOT NULL DEFAULT '' CHECK (char_length(collection) <= 40),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind, target_id),
    CHECK (kind = 'bookmark' OR collection = ''),
    CHECK (kind <> 'follow' OR user_id <> target_id)
);

CREATE INDEX np_engagement_target ON np_engagement(kind, target_id, user_id);
CREATE INDEX np_engagement_library ON np_engagement(user_id, kind, created_at DESC, target_id DESC);

COMMENT ON TABLE np_engagement IS 'Account bookmarks, likes and follows; target visibility is rechecked before public projection.';
