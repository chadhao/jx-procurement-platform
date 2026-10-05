-- 0021: N-061 T1 —— L01.核销后余额 进综合运营主管可写白名单（既有库更新路径）。
--
-- 与 0020 同款幂等模式（复用 H1 已落地路径）：只增不覆盖 —— NOT LIKE 守卫（已存在
-- 即跳过）；只动 resource='ledger:*' AND role='综合运营主管' 行（该列消费面）。
-- ★ 零 DDL：本迁移只 UPDATE 规则行，不涉及任何 t_ledger_* / t_submission 列结构。

UPDATE t_permission_rule
SET writable_fields = substr(writable_fields, 1, length(writable_fields) - 1) || ',"核销后余额"]'
WHERE role = '综合运营主管'
  AND resource = 'ledger:*'
  AND writable_fields IS NOT NULL
  AND writable_fields NOT LIKE '%核销后余额%';
