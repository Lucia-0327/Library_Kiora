-- 回滚仅移除遗失赔偿来源字段；历史修复插入的业务记录保留，避免丢失审计链路。
ALTER TABLE fines
    DROP CHECK chk_fines_origin,
    DROP INDEX idx_fines_loan_origin_status,
    DROP COLUMN origin;
