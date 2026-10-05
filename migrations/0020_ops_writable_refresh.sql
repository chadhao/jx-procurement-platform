-- 0020: N-060 H1② + H3 —— 综合运营主管 ledger 可写白名单刷新（既有库更新路径）。
--
-- 背景：SeedQ3Defaults 用 InsertPermissionRuleIfAbsent（INSERT OR IGNORE）⇒ 仅改
-- seed.go 对**既有库**无效。本迁移给出幂等更新路径（R-35 载体落点收口 + R-20#13
-- 两处过期名 + L06 存量三键补登记）。
--
-- 只增不覆盖（任务包硬要求）：
--   ① 旧名 → 权威名：REPLACE 定向改写（仅当旧名存在；改写后不再命中 LIKE ⇒ 幂等，
--      且**不动管理员已改的其它键**）；
--   ② 缺失键：json_array_append 逐个追加 + NOT LIKE 守卫（已存在即跳过 ⇒ 幂等）；
--   ③ 只作用 resource='ledger:*' AND role='综合运营主管' 的行（writable_fields 的
--      消费面＝台账 PATCH；其余资源行该列无消费方，保持管理员现值）。

-- ① 三处旧名 → 权威名（R-20#13：提交日期 / 集团受理编号；H3.3：付款完成日期）
UPDATE t_permission_rule
SET writable_fields = REPLACE(REPLACE(writable_fields,
        '"提交日期"', '"提交集团日期"'),
        '"集团受理编号"', '"集团流程编号"')
WHERE role = '综合运营主管'
  AND resource = 'ledger:*'
  AND writable_fields IS NOT NULL
  AND (writable_fields LIKE '%提交日期%' OR writable_fields LIKE '%集团受理编号%');

UPDATE t_permission_rule
SET writable_fields = REPLACE(writable_fields,
        '"付款完成日期"', '"付款 / 报销完成日期"')
WHERE role = '综合运营主管'
  AND resource = 'ledger:*'
  AND writable_fields IS NOT NULL
  AND writable_fields LIKE '%付款完成日期%';

-- ② 四个缺失键逐个追加（H3.2 两条 + H1 原件两条）—— NOT LIKE 守卫＝幂等且只增
UPDATE t_permission_rule
SET writable_fields = substr(writable_fields, 1, length(writable_fields) - 1) || ',"移交凭证（签收）"]'
WHERE role = '综合运营主管'
  AND resource = 'ledger:*'
  AND writable_fields IS NOT NULL
  AND writable_fields NOT LIKE '%移交凭证（签收）%';

UPDATE t_permission_rule
SET writable_fields = substr(writable_fields, 1, length(writable_fields) - 1) || ',"驳回原因与处置"]'
WHERE role = '综合运营主管'
  AND resource = 'ledger:*'
  AND writable_fields IS NOT NULL
  AND writable_fields NOT LIKE '%驳回原因与处置%';

UPDATE t_permission_rule
SET writable_fields = substr(writable_fields, 1, length(writable_fields) - 1) || ',"原件移交清单"]'
WHERE role = '综合运营主管'
  AND resource = 'ledger:*'
  AND writable_fields IS NOT NULL
  AND writable_fields NOT LIKE '%原件移交清单%';

UPDATE t_permission_rule
SET writable_fields = substr(writable_fields, 1, length(writable_fields) - 1) || ',"原件签收记录"]'
WHERE role = '综合运营主管'
  AND resource = 'ledger:*'
  AND writable_fields IS NOT NULL
  AND writable_fields NOT LIKE '%原件签收记录%';
