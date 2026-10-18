-- Клієнти. Одна строка = один співрозмовник в Instagram Direct.
CREATE TABLE contacts (
    id                 bigserial PRIMARY KEY,
    igsid              text NOT NULL UNIQUE,          -- Instagram-scoped user id
    username           text,                          -- @handle
    display_name       text,                          -- ім'я з профілю Instagram

    -- заповнюється автоматично (LLM) або руками
    full_name          text,                          -- ПІБ отримувача
    phone              text,
    city               text,
    np_branch          text,                          -- "Відділення №45" / "Поштомат №1234"
    sizes              text,                          -- "одяг M, взуття 38"
    last_interest      text,                          -- що цікавило востаннє
    notes              text,                          -- твої нотатки, сервіс їх не чіпає

    auto_update        boolean NOT NULL DEFAULT true, -- false = сервіс не перезаписує поля
    needs_review       boolean NOT NULL DEFAULT false,-- true = LLM щось змінив, глянь

    first_seen_at      timestamptz NOT NULL DEFAULT now(),
    last_message_at    timestamptz,
    extract_due_at     timestamptz,                   -- черга екстракції (debounce)
    profile_fetched_at timestamptz,
    profile_attempts   int NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX contacts_extract_due_idx ON contacts (extract_due_at) WHERE extract_due_at IS NOT NULL;
CREATE INDEX contacts_username_idx    ON contacts (lower(username));
CREATE INDEX contacts_phone_idx       ON contacts (phone);

-- Переписка.
CREATE TABLE messages (
    id          bigserial PRIMARY KEY,
    mid         text NOT NULL UNIQUE,                 -- id повідомлення від Meta (ідемпотентність)
    contact_id  bigint NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    direction   text NOT NULL CHECK (direction IN ('in', 'out')),
    text        text,
    attachments jsonb,                                -- URL тимчасові (Meta CDN), див. README
    sent_at     timestamptz NOT NULL,
    source      text NOT NULL DEFAULT 'webhook',      -- webhook | backfill
    raw         jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX messages_contact_sent_idx ON messages (contact_id, sent_at DESC);

-- Замовлення. Ведеш руками; дані отримувача підтягуються з контакту автоматично.
CREATE TABLE orders (
    id          bigserial PRIMARY KEY,
    contact_id  bigint NOT NULL REFERENCES contacts(id) ON DELETE RESTRICT,
    status      text NOT NULL DEFAULT 'new' CHECK (status IN
                ('new', 'awaiting_payment', 'paid', 'shipped', 'delivered', 'returned', 'cancelled')),
    items       text,
    amount      numeric(10, 2),
    ttn         text,
    full_name   text,
    phone       text,
    city        text,
    np_branch   text,
    comment     text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX orders_contact_idx ON orders (contact_id);
CREATE INDEX orders_status_idx  ON orders (status);

-- Журнал екстракцій: що LLM витягнув і що саме змінилось (історія старих значень).
CREATE TABLE extractions (
    id          bigserial PRIMARY KEY,
    contact_id  bigint NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    model       text,
    result      jsonb NOT NULL,
    changes     jsonb NOT NULL,                       -- {"city": {"old": "...", "new": "..."}}
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX extractions_contact_idx ON extractions (contact_id, created_at DESC);

-- Токен Instagram (одна строка).
CREATE TABLE ig_token (
    id           int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    access_token text NOT NULL,
    expires_at   timestamptz,
    refreshed_at timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- updated_at
CREATE FUNCTION touch_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END $$;

CREATE TRIGGER contacts_touch BEFORE UPDATE ON contacts FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER orders_touch   BEFORE UPDATE ON orders   FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Нове замовлення: порожні поля отримувача беруться з картки клієнта (знімок на момент замовлення).
CREATE FUNCTION orders_fill_recipient() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c contacts%ROWTYPE;
BEGIN
    SELECT * INTO c FROM contacts WHERE id = NEW.contact_id;
    NEW.full_name := COALESCE(NULLIF(NEW.full_name, ''), c.full_name);
    NEW.phone     := COALESCE(NULLIF(NEW.phone, ''),     c.phone);
    NEW.city      := COALESCE(NULLIF(NEW.city, ''),      c.city);
    NEW.np_branch := COALESCE(NULLIF(NEW.np_branch, ''), c.np_branch);
    RETURN NEW;
END $$;

CREATE TRIGGER orders_fill BEFORE INSERT ON orders FOR EACH ROW EXECUTE FUNCTION orders_fill_recipient();
