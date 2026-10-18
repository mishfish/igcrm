-- Довідкова таблиця статусів замовлення: замінює CHECK на справжній зв'язок (M2O),
-- потрібний для Directus Kanban (групування по dropdown-полю без зв'язку там не працює).
CREATE TABLE order_statuses (
    id    text PRIMARY KEY,
    label text NOT NULL,
    sort  int NOT NULL
);

INSERT INTO order_statuses (id, label, sort) VALUES
    ('new', 'Нове', 1),
    ('awaiting_payment', 'Очікує оплати', 2),
    ('paid', 'Оплачено', 3),
    ('materials_ready', 'Закуплено', 4),
    ('in_production', 'У роботі', 5),
    ('shipped', 'Відправлено', 6),
    ('delivered', 'Доставлено', 7),
    ('returned', 'Повернення', 8),
    ('cancelled', 'Скасовано', 9);

ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_fkey FOREIGN KEY (status) REFERENCES order_statuses(id);
