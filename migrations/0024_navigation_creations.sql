-- Creation receipts commit with menu, locations and audit. They intentionally
-- have no foreign key to menus: deletion and full-registry import retain the
-- original identity so late retries cannot resurrect a removed menu.
CREATE TABLE np_navigation_creations (
    key_hash text PRIMARY KEY CHECK (key_hash ~ '^[0-9a-f]{64}$'),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    menu_id bigint NOT NULL UNIQUE CHECK (menu_id > 0),
    created_at timestamptz NOT NULL
);
