-- Announcement documents use full SHA-256 revisions in the shared settings
-- table. Preserve the existing 16-character revision format for other keys.
ALTER TABLE np_site_settings DROP CONSTRAINT np_site_settings_revision_check;
ALTER TABLE np_site_settings ADD CONSTRAINT np_site_settings_revision_check CHECK (
    revision ~ '^[0-9a-f]{16}$'
    OR (settings_key = 'announcements' AND revision ~ '^[0-9a-f]{64}$')
);
