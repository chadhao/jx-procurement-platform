-- 0015_approval_submit_idem.sql —— 审批提交（M4）幂等键唯一索引。
--
-- ★ 只增不改历史（迁移纪律）。与 0002 的 ux_audit_idem_key 同款：
--   按调用方隔离（target_id + actor_open_id），部分唯一索引仅约束本 action。
-- ★ 沿用「幂等簿记寄存 t_audit_log」的既有模式（A1 已知债务；批 1 照抄 submission，
--   不顺手改表 —— 见批 1 方案风险清单第 4 条）。
-- ★ 语句顺序纪律见 submission/repo.go CreateWithIdem 注释：先占位、后建单；
--   本索引提供与并发模型无关的最终兜底。

CREATE UNIQUE INDEX IF NOT EXISTS ux_audit_idem_key_approval
  ON t_audit_log(target_id, actor_open_id)
  WHERE action = 'approval_submit_idem';
