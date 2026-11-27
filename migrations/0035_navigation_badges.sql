ALTER TABLE np_navigation_items
    ADD COLUMN IF NOT EXISTS badge jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(badge) = 'object' AND octet_length(badge::text) <= 2048);
