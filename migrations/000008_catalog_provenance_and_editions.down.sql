-- 回退会丢失来源、作品、出版社和译者等新增信息，只适用于尚未写入新模型数据的环境。
-- 如果已经导入了同名作者，恢复 full_name 唯一键前必须先人工完成作者合并。

DROP TABLE IF EXISTS book_translators;
DROP TABLE IF EXISTS book_publishers;
DROP TABLE IF EXISTS book_identifiers;

ALTER TABLE books
    DROP FOREIGN KEY fk_books_work,
    DROP FOREIGN KEY fk_books_source_provider,
    DROP FOREIGN KEY fk_books_primary_source_record,
    DROP CHECK chk_books_normalized_isbn,
    DROP CHECK chk_books_verification_status,
    DROP CHECK chk_books_collection_status,
    DROP CHECK chk_books_source_fields,
    DROP CHECK chk_books_source_matched_record,
    DROP CHECK chk_books_active_verified,
    DROP INDEX uq_books_normalized_isbn,
    DROP INDEX uq_books_source_edition,
    DROP INDEX uq_books_primary_source_record,
    DROP INDEX idx_books_work_id,
    DROP INDEX idx_books_source_work,
    DROP INDEX idx_books_catalog_status,
    DROP COLUMN work_id,
    DROP COLUMN normalized_isbn,
    DROP COLUMN source_provider,
    DROP COLUMN source_edition_key,
    DROP COLUMN source_work_key,
    DROP COLUMN source_revision,
    DROP COLUMN source_modified_at,
    DROP COLUMN source_url,
    DROP COLUMN publish_date_raw,
    DROP COLUMN edition_statement,
    DROP COLUMN language_code,
    DROP COLUMN primary_source_record_id,
    DROP COLUMN verification_status,
    DROP COLUMN collection_status;

ALTER TABLE authors
    DROP FOREIGN KEY fk_authors_source_provider,
    DROP CHECK chk_authors_source_identity,
    DROP INDEX uq_authors_source_key,
    DROP INDEX idx_authors_full_name,
    DROP COLUMN source_provider,
    DROP COLUMN source_author_key,
    ADD UNIQUE KEY uq_authors_full_name (full_name);

DROP TABLE IF EXISTS translators;
DROP TABLE IF EXISTS publishers;
DROP TABLE IF EXISTS catalog_source_records;
DROP TABLE IF EXISTS catalog_import_batches;
DROP TABLE IF EXISTS works;
DROP TABLE IF EXISTS catalog_sources;
