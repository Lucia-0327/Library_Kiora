-- 预约过期处理按状态等值、到期时间范围查找；复合索引避免每次流通操作扫描全部预约历史。
ALTER TABLE reservations
    ADD KEY idx_reservations_status_expires_at (status, expires_at);
