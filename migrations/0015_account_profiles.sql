ALTER TABLE np_users ADD COLUMN bio text NOT NULL DEFAULT '' CHECK (char_length(bio) <= 280);
-- Media objects live in the common local library in both storage modes.
ALTER TABLE np_users ADD COLUMN avatar_media_id bigint NOT NULL DEFAULT 0 CHECK (avatar_media_id >= 0);
CREATE INDEX np_users_avatar_media_idx ON np_users(avatar_media_id) WHERE avatar_media_id > 0;
CREATE INDEX np_comments_own_idx ON np_comments(user_id,updated_at DESC,id DESC) WHERE status IN ('approved','pending');
