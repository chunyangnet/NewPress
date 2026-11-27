CREATE TABLE np_messages (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sender_id bigint NOT NULL REFERENCES np_users(id),
    recipient_id bigint NOT NULL REFERENCES np_users(id),
    content text NOT NULL CHECK (length(content) BETWEEN 1 AND 2000),
    nonce varchar(80) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at timestamptz,
    CHECK (sender_id <> recipient_id),
    UNIQUE (sender_id, nonce)
);
CREATE INDEX np_messages_sender_thread ON np_messages(sender_id, recipient_id, id DESC);
CREATE INDEX np_messages_recipient_thread ON np_messages(recipient_id, sender_id, id DESC);
CREATE TABLE np_message_notifications (
    id bigint PRIMARY KEY REFERENCES np_messages(id),
    user_id bigint NOT NULL REFERENCES np_users(id),
    peer_id bigint NOT NULL REFERENCES np_users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at timestamptz
);
CREATE INDEX np_message_notifications_user ON np_message_notifications(user_id, id DESC);
