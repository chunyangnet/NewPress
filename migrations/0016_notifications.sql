-- Source outbox events are committed with the original content or comment.
ALTER TABLE np_outbox ADD COLUMN event_key varchar(160);
CREATE UNIQUE INDEX np_outbox_event_key_idx ON np_outbox(event_key);

CREATE TABLE np_notification_preferences (
    user_id bigint PRIMARY KEY REFERENCES np_users(id) ON DELETE CASCADE,
    version bigint NOT NULL DEFAULT 1,
    reply boolean NOT NULL DEFAULT true,
    mention boolean NOT NULL DEFAULT true,
    publication boolean NOT NULL DEFAULT true,
    moderation boolean NOT NULL DEFAULT true,
    email boolean NOT NULL DEFAULT false,
    weekly boolean NOT NULL DEFAULT false,
    email_since timestamptz,
    weekly_since timestamptz,
    unsubscribe_key varchar(64) NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE TABLE np_notification_receipts (
    event_key varchar(160) PRIMARY KEY,
    created_at timestamptz NOT NULL
);
CREATE TABLE np_notifications (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES np_users(id) ON DELETE CASCADE,
    kind varchar(32) NOT NULL,
    event jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    read_at timestamptz,
    cleared_at timestamptz
);
CREATE INDEX np_notifications_user_idx ON np_notifications(user_id,id DESC) WHERE cleared_at IS NULL;
CREATE TABLE np_notification_mail (
    key varchar(160) PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES np_users(id) ON DELETE CASCADE,
    kind varchar(32) NOT NULL,
    event jsonb NOT NULL,
    state varchar(16) NOT NULL CHECK (state IN ('pending','sending','sent','failed','uncertain','cancelled')),
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL,
    lease_until timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX np_notification_mail_due_idx ON np_notification_mail(available_at,key) WHERE state='pending';
CREATE INDEX np_notification_mail_user_idx ON np_notification_mail(user_id,created_at DESC);
