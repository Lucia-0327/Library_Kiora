-- 回滚资料类型和条码格式约束；已转换的条码值不自动恢复，避免破坏其唯一性。
ALTER TABLE book_copies
    DROP CHECK chk_book_copies_barcode;

ALTER TABLE books
    DROP CHECK chk_books_material_type,
    DROP INDEX idx_books_material_type,
    DROP COLUMN material_type;

ALTER TABLE books
    DROP FOREIGN KEY fk_books_category_required;

ALTER TABLE books MODIFY COLUMN category_id BIGINT UNSIGNED NULL;

ALTER TABLE books
    ADD CONSTRAINT fk_books_category FOREIGN KEY (category_id) REFERENCES categories (id) ON DELETE SET NULL;

ALTER TABLE categories
    DROP INDEX uq_categories_name;
