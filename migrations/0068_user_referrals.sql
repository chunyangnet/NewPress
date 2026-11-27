ALTER TABLE np_users ADD COLUMN referral jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(referral)='object');
CREATE INDEX np_users_referrer_idx ON np_users ((referral->>'inviter_id')) WHERE referral ? 'inviter_id';
