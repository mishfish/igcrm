-- Тип оплати, окремо від стадії виконання (status): повна передоплата,
-- часткова (напр. 50/50) чи оплата при отриманні (накладений платіж).
ALTER TABLE orders ADD COLUMN payment_type text
    CHECK (payment_type IN ('full_prepay', 'partial_prepay', 'cod'));
