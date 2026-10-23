-- Внутрішній Ref документа Нової Пошти (GUID, не номер ТТН) — потрібен лише для
-- InternetDocument/delete, якщо ТТН створили помилково і хочеш скасувати.
ALTER TABLE orders ADD COLUMN np_doc_ref text;
