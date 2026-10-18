-- 为系统自动生成的遗失赔偿增加来源标记，使状态修正只处理该类罚金而不影响馆员登记的其他罚金。
ALTER TABLE fines
    ADD COLUMN origin VARCHAR(20) NOT NULL DEFAULT 'manual' AFTER reason,
    ADD KEY idx_fines_loan_origin_status (loan_id, origin, status),
    ADD CONSTRAINT chk_fines_origin CHECK (origin IN ('manual', 'lost'));

-- 补齐历史遗失借阅：每条没有遗失赔偿罚金的记录补一笔 30 元未缴罚金，并冻结对应读者。
-- 使用 NOT EXISTS 使迁移可安全执行一次，且不会重复生成罚金。
INSERT INTO fines (loan_id, reader_id, amount, reason, origin, status)
SELECT l.id, l.reader_id, 30.00, '图书遗失赔偿（历史数据修复）', 'lost', 'unpaid'
FROM loans l
WHERE l.status = 'lost'
  AND NOT EXISTS (
      SELECT 1
      FROM fines f
      WHERE f.loan_id = l.id
        AND f.origin = 'lost'
  );

UPDATE readers r
LEFT JOIN (
    SELECT DISTINCT reader_id
    FROM fines
    WHERE status = 'unpaid'
) unpaid ON unpaid.reader_id = r.id
SET r.fine_blocked = unpaid.reader_id IS NOT NULL;
