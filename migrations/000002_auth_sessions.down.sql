-- 会话表依赖 users，因此应在 users 回滚前删除。
DROP TABLE IF EXISTS auth_sessions;
