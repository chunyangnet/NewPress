ALTER TABLE np_currency_entries ADD COLUMN purchase_amount bigint NOT NULL DEFAULT 0 CHECK (purchase_amount >= 0);
ALTER TABLE np_currency_entries DROP CONSTRAINT np_currency_entries_kind_check;
ALTER TABLE np_currency_entries DROP CONSTRAINT np_currency_entries_delta_check;
ALTER TABLE np_currency_entries DROP CONSTRAINT np_currency_entry_direction;
ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_entries_kind_check CHECK (kind IN
 ('admin_credit','admin_debit','credit','transfer_in','transfer_out','reward_check_in','reward_registration','reward_publication','purchase','purchase_cancel'));
ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_entries_delta_check CHECK (delta <> 0 OR kind='purchase_cancel');
ALTER TABLE np_currency_entries ADD CONSTRAINT np_currency_entry_direction CHECK (
 (purchase_amount=0 AND (
  (kind='admin_credit' AND delta>0 AND actor_id>0 AND peer_id IS NULL AND source_id=0)
  OR (kind='admin_debit' AND delta<0 AND delta>'-9223372036854775808'::bigint AND actor_id>0 AND peer_id IS NULL AND source_id=0)
  OR (kind='credit' AND delta>0 AND actor_id=0 AND peer_id IS NULL AND source_id=0)
  OR (kind='transfer_out' AND delta<0 AND delta>'-9223372036854775808'::bigint AND actor_id=user_id AND peer_id IS NOT NULL AND peer_id<>user_id AND source_id=0)
  OR (kind='transfer_in' AND delta>0 AND actor_id=peer_id AND peer_id IS NOT NULL AND peer_id<>user_id AND source_id=0)
  OR (kind='reward_check_in' AND delta>0 AND actor_id=user_id AND peer_id IS NULL AND source_id=0)
  OR (kind='reward_registration' AND delta>0 AND actor_id=0 AND peer_id IS NULL AND source_id=user_id)
  OR (kind='reward_publication' AND delta>0 AND actor_id=0 AND peer_id IS NULL AND source_id>0)
 )) OR (purchase_amount>0 AND actor_id=user_id AND peer_id IS NULL AND source_id=0 AND
  ((kind='purchase' AND delta=-purchase_amount) OR (kind='purchase_cancel' AND delta=0)))
);
ALTER TABLE np_currency_entries ADD UNIQUE(currency_key,id,user_id);

CREATE TABLE np_progress_settings (
 id smallint PRIMARY KEY CHECK(id=1),
 data jsonb NOT NULL CHECK(jsonb_typeof(data)='object' AND (data->>'version')::bigint>0)
);
INSERT INTO np_progress_settings(id,data) VALUES(1,'{
 "version":"1",
 "experience":{"enabled":false,"name":"经验值","maximum":"1000000000","daily_limit":"1000","require_verified_email":true},
 "levels":{"enabled":false,"public_badges":true,"items":[
  {"key":"beginner","name":"初来乍到","threshold":"0","color":"#64748b"},
  {"key":"reader","name":"常来常往","threshold":"100","color":"#287456"},
  {"key":"contributor","name":"热心分享","threshold":"500","color":"#2563eb"},
  {"key":"creator","name":"资深创作者","threshold":"2000","color":"#b57525"},
  {"key":"veteran","name":"社区之星","threshold":"5000","color":"#9f4561"}]},
 "check_in":{"enabled":false,"amount":"10","interval_hours":24},
 "registration":{"enabled":false,"amount":"100","since":"0001-01-01T00:00:00Z"},
 "publication":{"enabled":false,"amount":"50","min_length":300,"daily_limit":5,"since":"0001-01-01T00:00:00Z"},
 "membership":{"enabled":false,"public_badges":true,"plans":[
  {"key":"member","name":"标准会员","description":"会员徽章与经验加成","color":"#b57525","enabled":true,"days":30,"experience_percent":125,"purchase_enabled":false,"currency":"main","price":"100"},
  {"key":"supporter","name":"年度会员","description":"更长的会员有效期与经验加成","color":"#287456","enabled":true,"days":365,"experience_percent":150,"purchase_enabled":false,"currency":"main","price":"1000"}]}
}');

CREATE TABLE np_progress_accounts (
 user_id bigint PRIMARY KEY REFERENCES np_users(id) ON DELETE RESTRICT,
 experience bigint NOT NULL CHECK(experience>=0),
 membership jsonb NOT NULL CHECK(jsonb_typeof(membership)='object' AND (membership->>'version')::bigint>=0),
 pending_request text
);
CREATE TABLE np_progress_orders (
 user_id bigint NOT NULL REFERENCES np_users(id) ON DELETE RESTRICT,
 request_id text NOT NULL CHECK(request_id ~ '^[A-Za-z0-9_-]{16,80}$'),
 status text NOT NULL CHECK(status IN ('pending','completed','cancelled')),
 currency_key text NOT NULL REFERENCES np_currency_catalog(key) ON DELETE RESTRICT,
 payment_entry bigint,
 entry_id bigint,
 data jsonb NOT NULL CHECK(jsonb_typeof(data)='object'),
 PRIMARY KEY(user_id,request_id),
 UNIQUE(currency_key,payment_entry),
 FOREIGN KEY(currency_key,payment_entry,user_id) REFERENCES np_currency_entries(currency_key,id,user_id) ON DELETE RESTRICT,
 CHECK((status='pending' AND payment_entry IS NULL AND entry_id IS NULL)
    OR (status='cancelled' AND payment_entry IS NOT NULL AND entry_id IS NULL)
    OR (status='completed' AND payment_entry IS NOT NULL AND entry_id IS NOT NULL)),
 CHECK(data->>'status'=status AND data->>'currency'=currency_key AND data->>'request_id'=request_id AND (data->>'user_id')::bigint=user_id)
);
CREATE UNIQUE INDEX np_progress_one_pending ON np_progress_orders(user_id) WHERE status='pending';
ALTER TABLE np_progress_accounts ADD FOREIGN KEY(user_id,pending_request) REFERENCES np_progress_orders(user_id,request_id) ON DELETE RESTRICT;
CREATE TABLE np_progress_entries (
 id bigserial PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES np_users(id) ON DELETE RESTRICT,
 actor_id bigint NOT NULL CHECK(actor_id>=0),
 kind text NOT NULL CHECK(kind IN ('experience_adjust','reward_check_in','reward_registration','reward_publication','membership_grant','membership_renew','membership_revoke','membership_purchase')),
 created_at timestamptz NOT NULL,
 order_request text,
 data jsonb NOT NULL CHECK(jsonb_typeof(data)='object'),
 UNIQUE(id,user_id),
 FOREIGN KEY(user_id,order_request) REFERENCES np_progress_orders(user_id,request_id) ON DELETE RESTRICT,
 CHECK((data->>'id')::bigint=id AND (data->>'user_id')::bigint=user_id AND (data->>'actor_id')::bigint=actor_id AND data->>'kind'=kind AND (data->>'balance')::bigint>=0),
 CHECK((kind='experience_adjust' AND actor_id>0 AND (data->>'delta')::bigint<>0)
 OR (kind='reward_check_in' AND actor_id=user_id AND (data->>'delta')::bigint>0)
 OR (kind IN ('reward_registration','reward_publication') AND actor_id=0 AND (data->>'delta')::bigint>0)
 OR (kind LIKE 'membership_%' AND actor_id>0 AND (data->>'delta')::bigint=0)),
 CHECK((kind='membership_purchase' AND order_request IS NOT NULL AND actor_id=user_id) OR (kind<>'membership_purchase' AND order_request IS NULL))
);
ALTER TABLE np_progress_orders ADD FOREIGN KEY(entry_id,user_id) REFERENCES np_progress_entries(id,user_id) ON DELETE RESTRICT;
CREATE INDEX np_progress_history ON np_progress_entries(user_id,id DESC);
CREATE INDEX np_progress_rewards ON np_progress_entries(user_id,created_at) WHERE kind IN ('reward_check_in','reward_registration','reward_publication');
CREATE UNIQUE INDEX np_progress_source ON np_progress_entries(kind,(data->>'source_id')) WHERE kind IN ('reward_registration','reward_publication');
CREATE TABLE np_progress_receipts (
 actor_id bigint NOT NULL CHECK(actor_id>=0),
 request_id text NOT NULL CHECK(request_id ~ '^[A-Za-z0-9_-]{16,80}$'),
 fingerprint text NOT NULL CHECK(fingerprint ~ '^[a-f0-9]{64}$'),
 entry_id bigint NOT NULL REFERENCES np_progress_entries(id) ON DELETE RESTRICT,
 PRIMARY KEY(actor_id,request_id)
);
CREATE FUNCTION np_progress_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'progression history and receipts are append-only' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER np_progress_entries_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON np_progress_entries FOR EACH STATEMENT EXECUTE FUNCTION np_progress_immutable();
CREATE TRIGGER np_progress_receipts_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON np_progress_receipts FOR EACH STATEMENT EXECUTE FUNCTION np_progress_immutable();
CREATE TRIGGER np_progress_orders_retained BEFORE DELETE OR TRUNCATE ON np_progress_orders FOR EACH STATEMENT EXECUTE FUNCTION np_progress_immutable();
CREATE FUNCTION np_progress_settlement() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE payment np_currency_entries; history np_progress_entries;
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.status<>'pending' THEN RAISE EXCEPTION 'orders must start pending' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF OLD.status<>'pending' OR NEW.status='pending' OR OLD.user_id<>NEW.user_id OR OLD.request_id<>NEW.request_id OR OLD.currency_key<>NEW.currency_key
 OR (OLD.data - ARRAY['status','payment_entry','entry_id','completed_at']) IS DISTINCT FROM (NEW.data - ARRAY['status','payment_entry','entry_id','completed_at']) THEN
  RAISE EXCEPTION 'order terms are immutable' USING ERRCODE='23514';
 END IF;
 SELECT * INTO STRICT payment FROM np_currency_entries WHERE id=NEW.payment_entry AND currency_key=NEW.currency_key AND user_id=NEW.user_id;
 IF NOT EXISTS(SELECT 1 FROM np_currency_operations WHERE id=payment.operation_id AND currency_key=NEW.currency_key AND actor_id=NEW.user_id AND request_id=NEW.data->>'payment_request') THEN
  RAISE EXCEPTION 'order operation mismatch' USING ERRCODE='23514';
 END IF;
 IF payment.purchase_amount<>(NEW.data->>'amount')::bigint OR payment.reason<>NEW.data->>'payment_reason'
 OR (NEW.status='completed' AND payment.kind<>'purchase') OR (NEW.status='cancelled' AND payment.kind<>'purchase_cancel')
 OR (NEW.data->>'payment_entry')::bigint<>NEW.payment_entry THEN
  RAISE EXCEPTION 'order payment mismatch' USING ERRCODE='23514';
 END IF;
 IF NEW.status='completed' THEN
  SELECT * INTO STRICT history FROM np_progress_entries WHERE id=NEW.entry_id AND user_id=NEW.user_id;
  IF history.kind<>'membership_purchase' OR history.order_request<>NEW.request_id OR (NEW.data->>'entry_id')::bigint<>NEW.entry_id THEN
   RAISE EXCEPTION 'order history mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER np_progress_order_settlement BEFORE INSERT OR UPDATE ON np_progress_orders FOR EACH ROW EXECUTE FUNCTION np_progress_settlement();
