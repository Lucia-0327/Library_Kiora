-- 书目资料类型决定实体副本条码前缀：图书 BK，期刊 MC。
ALTER TABLE books
    ADD COLUMN material_type VARCHAR(20) NOT NULL DEFAULT 'book' AFTER isbn,
    ADD KEY idx_books_material_type (material_type),
    ADD CONSTRAINT chk_books_material_type CHECK (material_type IN ('book', 'periodical'));

-- 分类是书目业务必填项；分类名唯一，避免工作人员看到同名分类后无法区分。
ALTER TABLE categories
    ADD UNIQUE KEY uq_categories_name (name);

ALTER TABLE books DROP FOREIGN KEY fk_books_category;

ALTER TABLE books MODIFY COLUMN category_id BIGINT UNSIGNED NOT NULL;

ALTER TABLE books
    ADD CONSTRAINT fk_books_category_required FOREIGN KEY (category_id) REFERENCES categories (id) ON DELETE RESTRICT;

-- 先为全部历史副本按书目和原记录顺序重新编号，再增加格式约束。
-- 使用派生表而非临时表，迁移账号只需常规 DDL/DML 权限。
UPDATE book_copies AS c
JOIN (
    SELECT n.id,
           CONCAT(
               CASE WHEN n.material_type = 'periodical' THEN 'MC' ELSE 'BK' END,
               '-', YEAR(COALESCE(n.acquired_at, CURRENT_DATE)),
               '-', n.book_id,
               '-', LPAD(n.copy_sequence, 4, '0')
           ) AS new_barcode
    FROM (
        SELECT c2.id,
               c2.book_id,
               c2.acquired_at,
               b.material_type,
               ROW_NUMBER() OVER (PARTITION BY c2.book_id ORDER BY c2.id) AS copy_sequence
        FROM book_copies AS c2
        JOIN books AS b ON b.id = c2.book_id
    ) AS n
) AS g ON g.id = c.id
SET c.barcode = g.new_barcode;

ALTER TABLE book_copies
    ADD CONSTRAINT chk_book_copies_barcode CHECK (barcode REGEXP '^(BK|MC)-[0-9]{4}-[0-9]+-[0-9]{4}$');
