CREATE TABLE np_site_settings (
    settings_key text PRIMARY KEY CHECK (settings_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    revision text NOT NULL CHECK (revision ~ '^[0-9a-f]{16}$'),
    document jsonb NOT NULL CHECK (jsonb_typeof(document) = 'object')
);
