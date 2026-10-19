-- Розміри клієнта в замовленні: знімок з картки на момент створення, як і отримувач.
ALTER TABLE orders ADD COLUMN sizes text;

CREATE OR REPLACE FUNCTION orders_fill_recipient() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c contacts%ROWTYPE;
BEGIN
    SELECT * INTO c FROM contacts WHERE id = NEW.contact_id;
    NEW.full_name := COALESCE(NULLIF(NEW.full_name, ''), c.full_name);
    NEW.phone     := COALESCE(NULLIF(NEW.phone, ''),     c.phone);
    NEW.city      := COALESCE(NULLIF(NEW.city, ''),      c.city);
    NEW.np_branch := COALESCE(NULLIF(NEW.np_branch, ''), c.np_branch);
    NEW.sizes     := COALESCE(NULLIF(NEW.sizes, ''),     c.sizes);
    RETURN NEW;
END $$;

-- Вже створені замовлення: підтягнути поточні розміри з картки.
UPDATE orders o SET sizes = c.sizes
FROM contacts c
WHERE c.id = o.contact_id AND o.sizes IS NULL AND c.sizes IS NOT NULL;
