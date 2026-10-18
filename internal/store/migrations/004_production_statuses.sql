-- Два проміжні статуси між "paid" і "shipped" для пошиву на замовлення:
-- materials_ready — матеріали закуплені, можна починати; in_production — зараз шиється.
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK (status IN
    ('new', 'awaiting_payment', 'paid', 'materials_ready', 'in_production',
     'shipped', 'delivered', 'returned', 'cancelled'));
