-- 回滚预约过期查询索引。
ALTER TABLE reservations
    DROP INDEX idx_reservations_status_expires_at;
