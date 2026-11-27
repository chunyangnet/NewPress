-- Existing settings, balances, entries and receipts retain the main namespace.
-- The catalog never deletes or renames a key, even after a currency is disabled.
CREATE TABLE np_currency_catalog (
    key text PRIMARY KEY CHECK (key ~ '^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$'),
    actor_id bigint REFERENCES np_users(id) ON DELETE RESTRICT,
    request_id text,
    fingerprint char(64),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(actor_id, request_id),
    CHECK (
        (key = 'main' AND actor_id IS NULL AND request_id IS NULL AND fingerprint IS NULL)
        OR (key <> 'main' AND actor_id IS NOT NULL AND actor_id > 0 AND request_id IS NOT NULL
            AND request_id ~ '^[A-Za-z0-9_-]{16,80}$' AND fingerprint IS NOT NULL
            AND fingerprint ~ '^[a-f0-9]{64}$')
    )
);
INSERT INTO np_currency_catalog(key) VALUES ('main');

ALTER TABLE np_currency_settings ADD COLUMN currency_key text NOT NULL DEFAULT 'main'
    REFERENCES np_currency_catalog(key) ON DELETE RESTRICT;
ALTER TABLE np_currency_settings DROP CONSTRAINT np_currency_settings_pkey;
ALTER TABLE np_currency_settings ADD PRIMARY KEY (currency_key);

ALTER TABLE np_currency_balances ADD COLUMN currency_key text NOT NULL DEFAULT 'main'
    REFERENCES np_currency_catalog(key) ON DELETE RESTRICT;
ALTER TABLE np_currency_balances DROP CONSTRAINT np_currency_balances_pkey;
ALTER TABLE np_currency_balances ADD PRIMARY KEY (currency_key, user_id);

ALTER TABLE np_currency_operations ADD COLUMN currency_key text NOT NULL DEFAULT 'main'
    REFERENCES np_currency_catalog(key) ON DELETE RESTRICT;
ALTER TABLE np_currency_operations DROP CONSTRAINT np_currency_operations_actor_id_request_id_key;
ALTER TABLE np_currency_operations ADD UNIQUE (currency_key, actor_id, request_id);
ALTER TABLE np_currency_operations ADD UNIQUE (currency_key, id);

ALTER TABLE np_currency_entries ADD COLUMN currency_key text NOT NULL DEFAULT 'main'
    REFERENCES np_currency_catalog(key) ON DELETE RESTRICT;
ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_entry_scoped_operation
    FOREIGN KEY (currency_key, operation_id) REFERENCES np_currency_operations(currency_key, id) ON DELETE RESTRICT;

DROP INDEX np_currency_entries_user_history;
DROP INDEX np_currency_entries_user_transfers;
DROP INDEX np_currency_entries_reward_time;
CREATE INDEX np_currency_entries_user_history ON np_currency_entries(currency_key, user_id, id DESC);
CREATE INDEX np_currency_entries_user_transfers ON np_currency_entries(currency_key, user_id, created_at DESC)
    WHERE kind = 'transfer_out';
CREATE INDEX np_currency_entries_reward_time ON np_currency_entries(currency_key, created_at)
    WHERE kind IN ('reward_check_in','reward_registration','reward_publication');

CREATE TRIGGER np_currency_catalog_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
    ON np_currency_catalog FOR EACH STATEMENT EXECUTE FUNCTION np_currency_immutable_ledger();

COMMENT ON TABLE np_currency_catalog IS 'Immutable currency namespaces and idempotent administrator creation receipts. Disable a currency through its settings; never recycle its key.';
