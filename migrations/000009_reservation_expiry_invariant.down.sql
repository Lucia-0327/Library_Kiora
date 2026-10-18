-- 回滚预约期限约束；迁移时已经清理的历史残留期限不自动恢复。
ALTER TABLE reservations
    DROP CHECK chk_reservations_ready_expiry;
