-- 0019: t_instance / t_ledger_archive 行级权限**按真实列重建**（T04 权限位点 · N-042）
--       同款先例：0003 对 t_submission 的修复（assigned_open_id / acceptors）。
--       规格：docs/19-Permission-Points-Spec.md §3.1（DDL 与注释照抄该规格）。
-- 背景：两表原无「被指定经办人」与「验收人集合」列 ⇒ ASSIGNED / PARTICIPATED
--       只能一律 1=0（fail-closed，见 internal/permission/dataset.go），
--       使「采购经办人 / 验收人」两类角色在这两张表上恒见空集。
-- 注：SQLite 的 ALTER TABLE ... ADD COLUMN 不支持 IF NOT EXISTS；
--     由 t_schema_migrations 保证只执行一次（同 0003 注释口径）。

ALTER TABLE t_instance       ADD COLUMN designated_open_id TEXT;  -- 被指定经办人（ASSIGNED）
ALTER TABLE t_instance       ADD COLUMN acceptors        TEXT;  -- 验收人集合（PARTICIPATED，JSON 数组串）
ALTER TABLE t_ledger_archive ADD COLUMN designated_open_id TEXT;
ALTER TABLE t_ledger_archive ADD COLUMN acceptors        TEXT;

CREATE INDEX IF NOT EXISTS idx_instance_designated ON t_instance(designated_open_id);
CREATE INDEX IF NOT EXISTS idx_arch_designated     ON t_ledger_archive(designated_open_id);
