-- Every row change, including direct SQL and replica-role writes, gets a new
-- validation token. Readers verify this trigger contract before reusing bytes.
-- Tokens are transport hints, never authorization or operation identities.
ALTER TABLE np_plugin_webhook_intents
    ADD COLUMN validation_token uuid NOT NULL DEFAULT pg_catalog.gen_random_uuid();

CREATE FUNCTION np_plugin_webhook_validation_token() RETURNS trigger
    LANGUAGE plpgsql AS $body$
BEGIN
    NEW.validation_token := pg_catalog.gen_random_uuid();
    RETURN NEW;
END;
$body$;

CREATE TRIGGER np_plugin_webhook_validation_token
    BEFORE INSERT OR UPDATE ON np_plugin_webhook_intents
    FOR EACH ROW EXECUTE FUNCTION np_plugin_webhook_validation_token();
ALTER TABLE np_plugin_webhook_intents
    ENABLE ALWAYS TRIGGER np_plugin_webhook_validation_token;
