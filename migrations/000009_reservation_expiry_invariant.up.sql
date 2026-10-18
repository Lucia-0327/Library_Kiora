-- expires_at 表示当前待取预约的有效期限，不承担保存历史到期时间的职责。
-- 终态预约的历史发生时间由 updated_at 和审计日志记录，因此先清理旧演示数据中的残留值。
UPDATE reservations
SET expires_at = NULL
WHERE status <> 'ready'
  AND expires_at IS NOT NULL;

-- 只有已分配具体副本、等待读者领取的 ready 预约才必须具有取书期限。
ALTER TABLE reservations
    ADD CONSTRAINT chk_reservations_ready_expiry CHECK (
        (status = 'ready' AND expires_at IS NOT NULL)
        OR (status <> 'ready' AND expires_at IS NULL)
    );
