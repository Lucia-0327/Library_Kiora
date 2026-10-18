-- 回滚罚金读者归属和自动冻结标记；仅供人工恢复使用。
ALTER TABLE fines
    DROP FOREIGN KEY fk_fines_reader,
    DROP INDEX idx_fines_reader_status,
    DROP COLUMN reader_id;

ALTER TABLE readers
    DROP COLUMN fine_blocked;
