-- 0011_flow_op_log_round.sql —— 回调幂等键按**轮次（round）**区分。
--
-- ★ 修复的静默缺陷（本仓库头号红线「不报错但结果错/空」）：
--   回退（Rollback）复用同一 task_id —— `ResetFlowTaskTx` 只把 t_flow_task.round +1，
--   而 task_id 内嵌的 round 仍是旧值（task_id 一经生成就固定，04a §3.3 确定性生成）。
--   但 0007 的幂等唯一索引 ux_flow_op_callback 只含 (biz_no, task_id, op_type)：
--   回退后同一 task_id 再次 APPROVE/REJECT → 命中唯一索引 → `INSERT OR IGNORE` 吸收
--   → `HandleCallback` 返回 Duplicate=true → **不调 advancer** → 状态机静默不推进，
--   而飞书侧收到 200。应用内路径（act 忽略返回值、继续 advanceTx）不受影响
--   ⇒ 同一逻辑动作两条路径行为不一致。
--
-- 修法：幂等键加入 round。配套改动（缺一即退化回原缺陷）：
--   - t_flow_op_log 新增 round 列（既有行默认 0 ＝ 旧语义，历史键不受影响）；
--   - APPROVE/REJECT 两处写入点必须携带任务当前 round：
--     internal/flow/service.go（act，应用内路径）与 internal/flow/callback.go（recordCallback，回调路径）。
--   - 其余 op_type（TRANSFER/ADDSIGN/ROLLBACK/CANCEL/SUBMIT）不要求填 round（保持 0）。
--
-- ★ 幂等：ADD COLUMN 由 t_schema_migrations 保证只执行一次（与 0003/0007 同一约定）。
ALTER TABLE t_flow_op_log ADD COLUMN round INTEGER NOT NULL DEFAULT 0;

-- 重建部分唯一索引：键由 (biz_no, task_id, op_type) 扩为 (biz_no, task_id, op_type, round)。
DROP INDEX IF EXISTS ux_flow_op_callback;
CREATE UNIQUE INDEX IF NOT EXISTS ux_flow_op_callback
  ON t_flow_op_log(biz_no, task_id, op_type, round)
  WHERE op_type IN ('APPROVE', 'REJECT');
