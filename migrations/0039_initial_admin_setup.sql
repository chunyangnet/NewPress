CREATE TABLE np_installation_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    initialized boolean NOT NULL DEFAULT false,
    initialized_at timestamptz
);

INSERT INTO np_installation_state(singleton, initialized, initialized_at)
SELECT true, EXISTS (SELECT 1 FROM np_users WHERE status <> 'system'),
       CASE WHEN EXISTS (SELECT 1 FROM np_users WHERE status <> 'system') THEN now() ELSE NULL END
ON CONFLICT (singleton) DO NOTHING;

CREATE FUNCTION np_mark_installation_initialized() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status <> 'system' THEN
        UPDATE np_installation_state
        SET initialized = true, initialized_at = COALESCE(initialized_at, now())
        WHERE singleton = true;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER np_users_mark_installation_initialized
AFTER INSERT ON np_users
FOR EACH ROW EXECUTE FUNCTION np_mark_installation_initialized();
