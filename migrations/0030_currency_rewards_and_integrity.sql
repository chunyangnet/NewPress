ALTER TABLE np_currency_settings
    ADD COLUMN check_in_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN check_in_reward bigint NOT NULL DEFAULT 100 CHECK (check_in_reward > 0),
    ADD COLUMN check_in_interval_hours integer NOT NULL DEFAULT 24 CHECK (check_in_interval_hours BETWEEN 1 AND 720),
    ADD COLUMN registration_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN registration_reward bigint NOT NULL DEFAULT 1000 CHECK (registration_reward > 0),
    ADD COLUMN registration_since timestamptz,
    ADD COLUMN publication_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN publication_reward bigint NOT NULL DEFAULT 500 CHECK (publication_reward > 0),
    ADD COLUMN publication_since timestamptz,
    ADD COLUMN publication_min_length integer NOT NULL DEFAULT 300 CHECK (publication_min_length BETWEEN 1 AND 100000),
    ADD COLUMN publication_daily_limit integer NOT NULL DEFAULT 5 CHECK (publication_daily_limit BETWEEN 1 AND 1000),
    ADD COLUMN require_verified_email boolean NOT NULL DEFAULT true,
    ADD COLUMN daily_reward_limit bigint NOT NULL DEFAULT 10000 CHECK (daily_reward_limit > 0),
    ADD COLUMN daily_issuance_limit bigint NOT NULL DEFAULT 1000000 CHECK (daily_issuance_limit > 0);

UPDATE np_currency_settings SET daily_reward_limit = LEAST(daily_reward_limit, max_balance);

ALTER TABLE np_currency_settings
    ADD CONSTRAINT np_currency_limit_order CHECK (
        max_transfer <= max_balance AND daily_transfer_limit <= max_balance
        AND daily_reward_limit <= max_balance AND daily_issuance_limit >= daily_reward_limit
        AND (NOT check_in_enabled OR check_in_reward <= daily_reward_limit)
        AND (NOT registration_enabled OR registration_reward <= daily_reward_limit)
        AND (NOT publication_enabled OR publication_reward <= daily_reward_limit)
    );

ALTER TABLE np_currency_entries ADD COLUMN source_id bigint NOT NULL DEFAULT 0 CHECK (source_id >= 0);
ALTER TABLE np_currency_entries DROP CONSTRAINT np_currency_entries_kind_check;
ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_entries_kind_check CHECK (
    kind IN ('admin_credit','admin_debit','credit','transfer_in','transfer_out',
             'reward_check_in','reward_registration','reward_publication')
);

-- Deleting an account must never erase or rewrite the ledger it participated in.
ALTER TABLE np_currency_entries DROP CONSTRAINT np_currency_entries_user_id_fkey;
ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_entries_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES np_users(id) ON DELETE RESTRICT;
ALTER TABLE np_currency_entries DROP CONSTRAINT np_currency_entries_peer_id_fkey;
ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_entries_peer_id_fkey
    FOREIGN KEY (peer_id) REFERENCES np_users(id) ON DELETE RESTRICT;
ALTER TABLE np_currency_balances DROP CONSTRAINT np_currency_balances_user_id_fkey;
ALTER TABLE np_currency_balances ADD CONSTRAINT np_currency_balances_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES np_users(id) ON DELETE RESTRICT;

ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_entry_direction CHECK (
    (kind = 'admin_credit' AND delta > 0 AND actor_id > 0 AND peer_id IS NULL AND source_id = 0)
    OR (kind = 'admin_debit' AND delta < 0 AND delta > '-9223372036854775808'::bigint AND actor_id > 0 AND peer_id IS NULL AND source_id = 0)
    OR (kind = 'credit' AND delta > 0 AND actor_id = 0 AND peer_id IS NULL AND source_id = 0)
    OR (kind = 'transfer_out' AND delta < 0 AND delta > '-9223372036854775808'::bigint AND actor_id = user_id AND peer_id IS NOT NULL AND peer_id <> user_id AND source_id = 0)
    OR (kind = 'transfer_in' AND delta > 0 AND actor_id = peer_id AND peer_id IS NOT NULL AND peer_id <> user_id AND source_id = 0)
    OR (kind = 'reward_check_in' AND delta > 0 AND actor_id = user_id AND peer_id IS NULL AND source_id = 0)
    OR (kind = 'reward_registration' AND delta > 0 AND actor_id = 0 AND peer_id IS NULL AND source_id = user_id)
    OR (kind = 'reward_publication' AND delta > 0 AND actor_id = 0 AND peer_id IS NULL AND source_id > 0)
);

ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_operation_user_unique UNIQUE (operation_id, user_id);
ALTER TABLE np_currency_operations ADD CONSTRAINT np_currency_request_shape CHECK (request_id ~ '^[A-Za-z0-9_-]{16,80}$');
ALTER TABLE np_currency_operations ADD CONSTRAINT np_currency_fingerprint_shape CHECK (fingerprint ~ '^[a-f0-9]{64}$');
CREATE INDEX np_currency_entries_reward_time ON np_currency_entries(created_at)
    WHERE kind IN ('reward_check_in','reward_registration','reward_publication');

CREATE FUNCTION np_currency_immutable_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'currency ledger and operation receipts are append-only' USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER np_currency_entries_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
    ON np_currency_entries FOR EACH STATEMENT EXECUTE FUNCTION np_currency_immutable_ledger();
CREATE TRIGGER np_currency_operations_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
    ON np_currency_operations FOR EACH STATEMENT EXECUTE FUNCTION np_currency_immutable_ledger();
