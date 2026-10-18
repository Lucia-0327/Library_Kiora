-- 回滚品相状态和预约副本分配字段；已产生的馆藏状态及预约历史不自动复原。
ALTER TABLE reservations
    DROP CHECK chk_reservations_ready_copy,
    DROP FOREIGN KEY fk_reservations_reserved_copy,
    DROP INDEX uq_reservations_ready_copy,
    DROP INDEX idx_reservations_reserved_copy_id,
    DROP COLUMN reserved_copy_id;

ALTER TABLE book_copies
    DROP CHECK chk_book_copies_condition_status,
    DROP INDEX idx_book_copies_condition_status,
    DROP COLUMN condition_status;
