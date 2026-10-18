-- 读者账号不属于当前馆员管理台，读者只保留借阅档案。
-- 先解除绑定，再删除账号，保证 loans/reservations 的 reader_id 历史记录不受影响。
UPDATE readers r
JOIN users u ON u.id = r.user_id
SET r.user_id = NULL
WHERE u.role = 'reader';

UPDATE auth_sessions a
JOIN users u ON u.id = a.user_id
SET a.revoked_at = COALESCE(a.revoked_at, NOW())
WHERE u.role = 'reader';

DELETE FROM users WHERE role = 'reader';

ALTER TABLE readers
    DROP FOREIGN KEY fk_readers_user,
    DROP INDEX uq_readers_user_id,
    DROP COLUMN user_id;

ALTER TABLE users DROP CHECK chk_users_role;
ALTER TABLE users
    ADD CONSTRAINT chk_users_role CHECK (role IN ('admin', 'librarian'));
