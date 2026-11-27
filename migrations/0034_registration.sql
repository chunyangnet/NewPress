ALTER TABLE np_users
    ADD COLUMN IF NOT EXISTS phone text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS phone_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS external_key text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS registration_key text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS registration_digest text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS registration_version bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS password_login boolean NOT NULL DEFAULT true;

ALTER TABLE np_users DROP CONSTRAINT IF EXISTS np_users_email_trimmed;
ALTER TABLE np_users ADD CONSTRAINT np_users_email_trimmed CHECK (email='' OR (email=btrim(email) AND position('@' IN email)>1));
ALTER TABLE np_users DROP CONSTRAINT IF EXISTS np_users_email_key;
DROP INDEX IF EXISTS np_users_email_lower_uidx;
CREATE UNIQUE INDEX np_users_email_lower_uidx ON np_users(lower(email)) WHERE email<>'';
CREATE UNIQUE INDEX IF NOT EXISTS np_users_phone_uidx ON np_users(phone) WHERE phone<>'';
CREATE UNIQUE INDEX IF NOT EXISTS np_users_external_key_uidx ON np_users(external_key) WHERE external_key<>'';
CREATE UNIQUE INDEX IF NOT EXISTS np_users_registration_key_uidx ON np_users(registration_key) WHERE registration_key<>'';
ALTER TABLE np_users ADD CONSTRAINT np_users_phone_valid CHECK(phone='' OR phone ~ '^\+[1-9][0-9]{7,14}$');
