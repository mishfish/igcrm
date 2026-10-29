CREATE TABLE tg_users (
    tg_user_id    bigint PRIMARY KEY,
    chat_id       bigint NOT NULL,
    username      text,
    registered_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tg_bot_state (
    id             int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_update_id bigint NOT NULL DEFAULT 0
);
INSERT INTO tg_bot_state (id, last_update_id) VALUES (1, 0);

CREATE TABLE bot_actions (
    id         bigserial PRIMARY KEY,
    tg_user_id bigint NOT NULL,
    action     text NOT NULL,
    contact_id bigint REFERENCES contacts(id) ON DELETE SET NULL,
    order_id   bigint REFERENCES orders(id) ON DELETE SET NULL,
    detail     jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bot_actions_created_idx ON bot_actions (created_at DESC);
