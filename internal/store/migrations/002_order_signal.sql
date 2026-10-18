-- true, якщо замовлення створив сервіс (LLM розпізнав підтвердження в переписці),
-- а не ти руками в Directus. Допомагає зрозуміти, що варто перевірити.
ALTER TABLE orders ADD COLUMN auto_created boolean NOT NULL DEFAULT false;
