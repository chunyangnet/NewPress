CREATE TABLE np_oauth_bindings (
    user_id bigint NOT NULL REFERENCES np_users(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('github','google','microsoft','linuxdo','qq','wechat')),
    external_key text NOT NULL UNIQUE CHECK (external_key ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (user_id, provider)
);
ALTER TABLE np_users DROP COLUMN external_key;
