-- 0003: 报送登记（t_submission）行级权限**按真实列重建**
--       （Q14-B 第 5 项，2026-09-26 决定：按真实列重建，取消降级）
--
-- 背景：t_submission 原无部门 / 申请人列，导致 DEPT / CHARGE_DEPT / ASSIGNED / PARTICIPATED
-- 四个 row_scope 令牌只能统一降级为 `created_by = me`（保守收紧，见 docs/06 B21）。
-- 补上真实列后，四个令牌恢复其应有语义；未知 / DENY / 空身份仍 fail-closed（1=0）。
--
-- 注：SQLite 的 `ALTER TABLE ... ADD COLUMN` 不支持 IF NOT EXISTS；
--     本文件由 t_schema_migrations 保证只执行一次（见 internal/store/migrate.go）。

ALTER TABLE t_submission ADD COLUMN department        TEXT;  -- 申请人所属部门（DEPT / CHARGE_DEPT）
ALTER TABLE t_submission ADD COLUMN applicant_open_id TEXT;  -- 申请人（SELF）
ALTER TABLE t_submission ADD COLUMN assigned_open_id  TEXT;  -- 被指定经办人（ASSIGNED）
ALTER TABLE t_submission ADD COLUMN acceptors         TEXT;  -- 验收人集合（PARTICIPATED，JSON 数组串）

CREATE INDEX IF NOT EXISTS idx_submission_dept      ON t_submission(department);
CREATE INDEX IF NOT EXISTS idx_submission_applicant ON t_submission(applicant_open_id);
