-- 0009_flow_task_order.sql —— 架构转向 ③：t_flow_task 补「同节点内审批人声明序」显式列。
--
-- ★ 为什么需要：顺序会签「同 node_id 按次序逐级释放」（docs/04a §2.3），但本仓 `node_seq`
--   语义＝**节点顺序**（同节点各任务 node_seq 相同）→ **无法区分同节点内的审批人**。
--   此前想用 `rowid` 兜底，但 `rowid` **不是稳定契约**：`INSERT OR REPLACE`、重建表、`VACUUM`
--   都会改变它。把「审批顺序」绑在实现细节上，一旦漂移**不报错、只是"该谁审批"变了** —— 静默族。
--   故新增显式列 `task_order` 作为**唯一顺序契约**。
--
-- ★ 本期只做「增」，不改既有列语义、不删除。
-- ★ 幂等：ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS，靠 t_schema_migrations 只执行一次。

-- task_order：同节点内审批人**声明序**（1-based）。节点间顺序仍由 node_seq 表达。
ALTER TABLE t_flow_task ADD COLUMN task_order INTEGER NOT NULL DEFAULT 0;

-- ★ 一次性存量回填（无生产数据）：把已有行的 task_order 近似填成插入序（rowid）。
--   ★ 此举**仅用于消化存量**；`task_order` 自此是**唯一**顺序契约，
--   **禁止任何实现回退到按 `rowid` 排序**（rowid 会随 REPLACE/VACUUM 漂移）。
--   新行一律由 createTasksTx 显式写入 1-based 的按序值。
UPDATE t_flow_task SET task_order = rowid WHERE task_order = 0;
