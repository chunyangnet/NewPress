CREATE TABLE IF NOT EXISTS np_navigation_menus (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    version bigint NOT NULL DEFAULT 1,
    name varchar(240) NOT NULL,
    slug varchar(288) NOT NULL,
    description varchar(960) NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT np_navigation_menus_version_positive CHECK (version > 0),
    CONSTRAINT np_navigation_menus_name_bounded CHECK (octet_length(name) BETWEEN 1 AND 720),
    CONSTRAINT np_navigation_menus_slug_bounded CHECK (octet_length(slug) BETWEEN 1 AND 288),
    CONSTRAINT np_navigation_menus_description_bounded CHECK (octet_length(description) <= 2880)
);

CREATE UNIQUE INDEX IF NOT EXISTS np_navigation_menus_slug_lower_uidx
    ON np_navigation_menus (lower(slug));

CREATE TABLE IF NOT EXISTS np_navigation_locations (
    location varchar(24) PRIMARY KEY,
    menu_id bigint NOT NULL REFERENCES np_navigation_menus(id) ON DELETE CASCADE,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT np_navigation_locations_valid CHECK (location IN ('primary', 'utility', 'mobile', 'footer'))
);

CREATE INDEX IF NOT EXISTS np_navigation_locations_menu_idx
    ON np_navigation_locations (menu_id, location);

CREATE TABLE IF NOT EXISTS np_navigation_items (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    menu_id bigint NOT NULL REFERENCES np_navigation_menus(id) ON DELETE CASCADE,
    parent_id bigint,
    position integer NOT NULL DEFAULT 0,
    item_type varchar(24) NOT NULL DEFAULT 'custom',
    object_id bigint,
    label varchar(240) NOT NULL,
    url varchar(2048) NOT NULL,
    description varchar(720) NOT NULL DEFAULT '',
    target varchar(12) NOT NULL DEFAULT 'self',
    rel varchar(96) NOT NULL DEFAULT '',
    icon varchar(32) NOT NULL DEFAULT '',
    accent varchar(7) NOT NULL DEFAULT '',
    presentation varchar(24) NOT NULL DEFAULT 'standard',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT np_navigation_items_menu_id_id_unique UNIQUE (menu_id, id),
    CONSTRAINT np_navigation_items_parent_same_menu FOREIGN KEY (menu_id, parent_id)
        REFERENCES np_navigation_items(menu_id, id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT np_navigation_items_parent_not_self CHECK (parent_id IS NULL OR parent_id <> id),
    CONSTRAINT np_navigation_items_position_nonnegative CHECK (position >= 0),
    CONSTRAINT np_navigation_items_type_valid CHECK (item_type IN ('custom', 'post', 'page', 'category', 'tag')),
    CONSTRAINT np_navigation_items_object_valid CHECK (
        (item_type = 'custom' AND object_id IS NULL)
        OR (item_type <> 'custom' AND object_id IS NOT NULL AND object_id > 0)
    ),
    CONSTRAINT np_navigation_items_label_bounded CHECK (octet_length(label) BETWEEN 1 AND 720),
    CONSTRAINT np_navigation_items_url_bounded CHECK (octet_length(url) BETWEEN 1 AND 2048),
    CONSTRAINT np_navigation_items_description_bounded CHECK (octet_length(description) <= 2160),
    CONSTRAINT np_navigation_items_target_valid CHECK (target IN ('self', 'blank')),
    CONSTRAINT np_navigation_items_presentation_valid CHECK (presentation IN ('standard', 'dropdown', 'panel', 'cards')),
    CONSTRAINT np_navigation_items_accent_valid CHECK (accent = '' OR accent ~ '^#[0-9A-F]{6}$'),
    CONSTRAINT np_navigation_items_icon_valid CHECK (icon = '' OR icon ~ '^[a-z0-9][a-z0-9-]{0,31}$')
);

CREATE INDEX IF NOT EXISTS np_navigation_items_menu_order_idx
    ON np_navigation_items (menu_id, position, id);

CREATE INDEX IF NOT EXISTS np_navigation_items_parent_order_idx
    ON np_navigation_items (menu_id, parent_id, position, id);
