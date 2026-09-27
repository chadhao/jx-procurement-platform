-- 0008_flow_task_release.sql —— 架构转向 ③：t_flow_task 补「顺序会签 · 分段释放」两列。
-- 依据 docs/04a-Architecture-Increment-V2.md §1.1（t_flow_task 列清单）与 §2.3（默认＝顺序会签，逐级释放）。
--
-- ★ 为什么必须新增 0008、不得回改 0007：0007 已在 dev 库应用（t_schema_migrations 有记录），
--   「已应用迁移不得回改」是硬纪律；补列只能走新迁移（04a §13 改动点明示）。
--
-- ★ 本期只做「增」：只加两列，不改既有列语义、不删任何列/表。
--
-- ★ 幂等：ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS，靠 t_schema_migrations 只执行一次
--   （与 0003 / 0007 同一约定）。重复执行 Migrate(ctx, db) 不会报错。

-- ============ release_state：分段释放（HELD → RELEASED） ============
-- 取值：HELD（未释放；飞书侧不推、不生成待办）/ RELEASED（已释放；当前可办理）。
-- 一期所有会签节点一律「顺序会签」：同 node_id 内逐级释放，上一位通过后才释放下一位
-- （docs/04a §2.3；01a §4.3 用户定案「加签是顺序会签，上个人审批之后，下个人才能收到通知」）。
--
-- ★ 为何 DEFAULT 'HELD'（而非 04a §13 草拟的 DEFAULT 'RELEASED'）：新语义下「未释放」才是
--   会签节点的默认态（只有第一个任务应在提交时被释放）。默认 HELD 能防「新行漏写 release_state
--   却被当成可办理」的静默放行；代价是——必须配下方回填，否则存量行会被冻结（见下）。
ALTER TABLE t_flow_task ADD COLUMN release_state TEXT NOT NULL DEFAULT 'HELD';

-- ============ weight：票签 / 并行会签扩展位（一期恒 NULL） ============
-- 一期「加签＝会签＝顺序会签，不可配为并签」（01a §4.6 定案）；weight 仅保留二期扩展位，
-- 本期不开口子（引入「可配并行」会让「逐级释放」的实现分叉）。
ALTER TABLE t_flow_task ADD COLUMN weight INTEGER;

-- ★★★ 存量行回填（关键：缺之会「冻结存量任务、审批走不动」，且不报错）★★★
--   0007 之前的 createTasksTx 是「提交时一次建全链、全部可操作」的并行语义——这些既有任务
--   在旧语义下当前均可办理。若不回填，它们会带着刚加的 DEFAULT 'HELD' 变成「未释放」，
--   导致既有在途审批全部卡死（办理时被顺序会签门禁拒绝，且初始无任何报错线索）。
--   故一次性把既有行置为 RELEASED（＝旧语义下它们本就「已可办理」）。
UPDATE t_flow_task SET release_state = 'RELEASED' WHERE release_state = 'HELD';
