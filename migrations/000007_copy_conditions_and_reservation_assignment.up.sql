-- 品相与流通状态分开维护：历史记录无法可靠判定品相，先标记为待检查；新录入副本默认完好。
ALTER TABLE book_copies
    ADD COLUMN condition_status VARCHAR(32) NOT NULL DEFAULT 'pending_inspection' AFTER condition_note,
    ADD KEY idx_book_copies_condition_status (condition_status),
    ADD CONSTRAINT chk_book_copies_condition_status CHECK (condition_status IN ('intact', 'minor_wear', 'damaged', 'pending_inspection'));

-- 仅转换系统过去写入的固定演示备注；其他自由文本和空值一律保守地保留为待检查。
UPDATE book_copies
SET condition_status = CASE
    WHEN condition_note IN ('馆藏完好', '完好') THEN 'intact'
    WHEN condition_note IN ('封面轻微磨损', '轻微磨损') THEN 'minor_wear'
    WHEN condition_note IN ('等待修复装订', '破损', '损坏') THEN 'damaged'
    ELSE 'pending_inspection'
END;

ALTER TABLE book_copies
    MODIFY COLUMN condition_status VARCHAR(32) NOT NULL DEFAULT 'intact';

-- 待取预约必须记录具体被占用的副本，才能在副本盘点遗失时准确释放并重新分配。
ALTER TABLE reservations
    ADD COLUMN reserved_copy_id BIGINT UNSIGNED NULL AFTER book_id,
    ADD KEY idx_reservations_reserved_copy_id (reserved_copy_id);

-- 按同一书目的队列顺序和预留副本顺序回填历史待取记录；双层派生表避免更新目标表读取限制。
UPDATE reservations AS r
JOIN (
    SELECT *
    FROM (
        SELECT ready_rows.reservation_id, reserved_rows.copy_id
        FROM (
            SELECT r2.id AS reservation_id,
                   r2.book_id,
                   ROW_NUMBER() OVER (PARTITION BY r2.book_id ORDER BY r2.queue_position, r2.id) AS sequence_no
            FROM reservations AS r2
            WHERE r2.status = 'ready'
        ) AS ready_rows
        JOIN (
            SELECT c2.id AS copy_id,
                   c2.book_id,
                   ROW_NUMBER() OVER (PARTITION BY c2.book_id ORDER BY c2.id) AS sequence_no
            FROM book_copies AS c2
            WHERE c2.status = 'reserved'
        ) AS reserved_rows
          ON reserved_rows.book_id = ready_rows.book_id
         AND reserved_rows.sequence_no = ready_rows.sequence_no
    ) AS reservation_copy_mapping
) AS mapping ON mapping.reservation_id = r.id
SET r.reserved_copy_id = mapping.copy_id
WHERE r.status = 'ready';

-- 无法回填具体副本的历史待取记录退回排队，避免读者领取到没有明确分配的副本。
UPDATE reservations
SET status = 'waiting', expires_at = NULL, reserved_copy_id = NULL
WHERE status = 'ready' AND reserved_copy_id IS NULL;

-- 品相不合格的待取副本先释放其预约；后续会从仍可借的副本中自动补位。
UPDATE reservations AS r
JOIN book_copies AS c ON c.id = r.reserved_copy_id
SET r.status = 'waiting', r.expires_at = NULL, r.reserved_copy_id = NULL
WHERE r.status = 'ready'
  AND c.condition_status IN ('damaged', 'pending_inspection');

-- 破损和待检查副本不得继续处于可借或待取状态。
UPDATE book_copies
SET status = 'maintenance'
WHERE status IN ('available', 'reserved')
  AND condition_status IN ('damaged', 'pending_inspection');

-- 清理没有待取预约的预留副本，使其按品相恢复可借或维护。
UPDATE book_copies AS c
LEFT JOIN reservations AS r
  ON r.reserved_copy_id = c.id
 AND r.status = 'ready'
SET c.status = CASE
    WHEN c.condition_status IN ('intact', 'minor_wear') THEN 'available'
    ELSE 'maintenance'
END
WHERE c.status = 'reserved'
  AND r.id IS NULL;

-- 只为每个书目的队首等待读者补回一册可借副本，保持原有“每个书目仅一位待取”的预约规则。
UPDATE reservations AS r
JOIN (
    SELECT *
    FROM (
        SELECT waiting_rows.reservation_id, available_rows.copy_id
        FROM (
            SELECT r2.id AS reservation_id,
                   r2.book_id,
                   ROW_NUMBER() OVER (PARTITION BY r2.book_id ORDER BY r2.queue_position, r2.id) AS sequence_no
            FROM reservations AS r2
            WHERE r2.status = 'waiting'
        ) AS waiting_rows
        JOIN (
            SELECT c2.id AS copy_id,
                   c2.book_id,
                   ROW_NUMBER() OVER (PARTITION BY c2.book_id ORDER BY c2.id) AS sequence_no
            FROM book_copies AS c2
            WHERE c2.status = 'available'
              AND c2.condition_status IN ('intact', 'minor_wear')
        ) AS available_rows
          ON available_rows.book_id = waiting_rows.book_id
         AND available_rows.sequence_no = 1
        LEFT JOIN (
            SELECT DISTINCT book_id
            FROM reservations
            WHERE status = 'ready'
        ) AS ready_books ON ready_books.book_id = waiting_rows.book_id
        WHERE waiting_rows.sequence_no = 1
          AND ready_books.book_id IS NULL
    ) AS waiting_copy_mapping
) AS mapping ON mapping.reservation_id = r.id
SET r.status = 'ready',
    r.expires_at = DATE_ADD(NOW(), INTERVAL 3 DAY),
    r.reserved_copy_id = mapping.copy_id
WHERE r.status = 'waiting';

UPDATE book_copies AS c
JOIN reservations AS r
  ON r.reserved_copy_id = c.id
 AND r.status = 'ready'
SET c.status = 'reserved'
WHERE c.status = 'available';

ALTER TABLE reservations
    ADD UNIQUE KEY uq_reservations_ready_copy (reserved_copy_id),
    ADD CONSTRAINT fk_reservations_reserved_copy FOREIGN KEY (reserved_copy_id) REFERENCES book_copies (id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_reservations_ready_copy CHECK (
        (status = 'ready' AND reserved_copy_id IS NOT NULL)
        OR (status <> 'ready' AND reserved_copy_id IS NULL)
    );
