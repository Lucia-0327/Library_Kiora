-- 将每笔罚金明确归属到读者档案，并为已有记录补齐读者。
ALTER TABLE fines
    ADD COLUMN reader_id BIGINT UNSIGNED NULL AFTER loan_id,
    ADD KEY idx_fines_reader_status (reader_id, status);

UPDATE fines f
JOIN loans l ON l.id = f.loan_id
SET f.reader_id = l.reader_id
WHERE f.reader_id IS NULL;

ALTER TABLE fines
    MODIFY COLUMN reader_id BIGINT UNSIGNED NOT NULL,
    ADD CONSTRAINT fk_fines_reader FOREIGN KEY (reader_id) REFERENCES readers (id) ON DELETE RESTRICT;

-- fine_blocked 用于区分因未结罚金自动冻结和馆员手工暂停，缴清后不会误改手工状态。
ALTER TABLE readers
    ADD COLUMN fine_blocked BOOLEAN NOT NULL DEFAULT FALSE AFTER status;

UPDATE readers r
LEFT JOIN (
    SELECT DISTINCT reader_id
    FROM fines
    WHERE status = 'unpaid'
) unpaid ON unpaid.reader_id = r.id
SET r.fine_blocked = unpaid.reader_id IS NOT NULL;
