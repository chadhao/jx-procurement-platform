-- 0007_approval_core.sql —— 架构转向 ③（审批核心迁至我方）地基层。
-- 依据 docs/04a-Architecture-Increment-V2.md §1.1 表清单（6 新表 + t_instance 增列）。
--
-- ★ 本期只做「增」：只建新表 / 只加新列，**不作废、不删除**任何既有表与列，
--   既有原生控件解析链（parseForm / valueToText / extract.go）、事件订阅路径保持原样。
--
-- ★ 幂等：CREATE TABLE/INDEX 用 IF NOT EXISTS；ALTER TABLE ADD COLUMN 由 t_schema_migrations
--   保证只执行一次（与 0003 同一约定）。重复执行 Migrate(ctx, db) 不会报错。
--
-- ★ 锁号三前提（04a §6.4，破坏即**静默失效**：台账/审计出现「同号两笔」）：
--   P1 t_doc_seq **单调递增、只增不减、不参与任何归档/清理**（scripts/archive-year.sh 已加自检门禁）；
--   P2 t_instance 及台账**不做硬删除**，终态只改状态（撤回=CANCELED 仍占号）；
--   P3 下方 ux_instance_biz_no **UNIQUE(biz_no)** 为最终兜底。
--   任一条被破坏，终态单号即可能被复用，且**不报错**。

-- ============ 单据编号器（04a §6 / t_doc_seq） ============
-- PK(doc_type, yymm)：按月重置；last_seq 单调递增。**此表不参与归档/清理**（前提 P1）。
CREATE TABLE IF NOT EXISTS t_doc_seq (
  doc_type   TEXT    NOT NULL,               -- 单据类型（BA/PR/SA/RFQ/BJ/SS/CT/PC/GR/QC/SUB；PO 归并为 CT）
  yymm       TEXT    NOT NULL,               -- 业务本地年月 YYMM（Asia/Shanghai）
  last_seq   INTEGER NOT NULL DEFAULT 0,     -- 已用到的最大序号（只增不减）
  updated_at TEXT    NOT NULL,
  PRIMARY KEY (doc_type, yymm)
);

-- ============ 我方任务 / 节点（04a §1.1 / §1.2，飞书 task_list 的真源） ============
-- task_id 为**确定性**生成（{node_id}-{assignee}-{round}-{seq}），重推同一快照得同一 ID
-- （04a §3.3：ID 重复会让「审批中心看不到数据」，且静默）。
CREATE TABLE IF NOT EXISTS t_flow_task (
  task_id          TEXT PRIMARY KEY,          -- 确定性 task_id
  biz_no           TEXT NOT NULL,             -- 关联业务单号（一实例多任务）
  node_id          TEXT NOT NULL,             -- 节点标识（会签聚合按此分组，04a §2.3）
  node_name        TEXT,
  node_seq         INTEGER NOT NULL DEFAULT 0,-- 节点顺序（推进判定用）
  round            INTEGER NOT NULL DEFAULT 1,-- 回退重激活轮次
  assignee_open_id TEXT NOT NULL,             -- 审批人
  assignee_name    TEXT,
  status           TEXT NOT NULL,             -- PENDING/APPROVED/REJECTED/TRANSFERRED/DONE
  action_context   TEXT,                      -- 回调定位用（原样回传，04a §4.2）
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  closed_at        TEXT
);
CREATE INDEX IF NOT EXISTS idx_flow_task_biz      ON t_flow_task(biz_no);
CREATE INDEX IF NOT EXISTS idx_flow_task_node     ON t_flow_task(biz_no, node_id);
CREATE INDEX IF NOT EXISTS idx_flow_task_assignee ON t_flow_task(assignee_open_id, status);

-- ============ 操作留痕（04a §1.1 / §5.1；四操作 + 回调 + 提交） ============
CREATE TABLE IF NOT EXISTS t_flow_op_log (
  op_id         INTEGER PRIMARY KEY AUTOINCREMENT,
  biz_no        TEXT NOT NULL,
  node_id       TEXT,
  task_id       TEXT,
  op_type       TEXT NOT NULL,                -- SUBMIT/APPROVE/REJECT/TRANSFER/ADDSIGN/ROLLBACK/CANCEL
  actor_open_id TEXT,
  from_status   TEXT,
  to_status     TEXT,
  reason        TEXT,
  extra_json    TEXT,
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_flow_op_biz ON t_flow_op_log(biz_no, created_at);
-- ★ 回调幂等（04a §4.3）：仅对 APPROVE/REJECT 建 (biz_no,task_id,op_type) **部分唯一索引**。
--   超时口径不一（QV2-02）→ 重复回调必然发生；无此约束会**二次推进状态机**。
CREATE UNIQUE INDEX IF NOT EXISTS ux_flow_op_callback
  ON t_flow_op_log(biz_no, task_id, op_type)
  WHERE op_type IN ('APPROVE', 'REJECT');

-- ============ 三方审批定义注册表（04a §1.1 / §3 / §6） ============
-- approval_code 命中即更新、未命中即新建（平台侧由 external_approvals 决定）；
-- 本地以 approval_code 为 PK，并令 doc_type 唯一，保证「重复注册=更新，不产生第二条定义」。
CREATE TABLE IF NOT EXISTS t_approval_def (
  approval_code      TEXT PRIMARY KEY,
  doc_type           TEXT NOT NULL,           -- 我方单据类型（11 类）
  name               TEXT NOT NULL,
  group_name         TEXT,
  visible_scope_json TEXT,                    -- 可见范围（原样 JSON 片段）
  create_link_pc     TEXT,                    -- 发起页（PC，指向我方页面）
  create_link_mobile TEXT,                    -- 发起页（Mobile）
  callback_url       TEXT,                    -- action_callback_url（入站回调端点）
  callback_token     TEXT,                    -- action_callback_token
  callback_key       TEXT,                    -- action_callback_key
  form_summary_json  TEXT,                    -- 飞书列表摘要（3 条）配置
  def_version        INTEGER NOT NULL DEFAULT 1,
  updated_at         TEXT NOT NULL
);
-- doc_type 唯一（一单据类型仅一个三方定义）→ 支撑「重复注册=更新，不产生第二条定义」。
CREATE UNIQUE INDEX IF NOT EXISTS ux_approval_def_doc_type ON t_approval_def(doc_type);

-- ============ 推送流水 + 幂等（04a §1.1 / §3 / §10） ============
CREATE TABLE IF NOT EXISTS t_push_record (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  biz_no        TEXT NOT NULL,
  push_seq      INTEGER NOT NULL,             -- 推送序号（= 推送时的 update_time）
  snapshot_hash TEXT,
  status        TEXT NOT NULL,                -- PENDING/SENT/FAILED
  attempts      INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT,
  created_at    TEXT NOT NULL,
  sent_at       TEXT,
  UNIQUE(biz_no, push_seq)
);
CREATE INDEX IF NOT EXISTS idx_push_record_biz ON t_push_record(biz_no, push_seq);

-- ============ 通知流水（04a §1.1 / §5.5；漏发可检出） ============
CREATE TABLE IF NOT EXISTS t_notify_log (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  biz_no         TEXT NOT NULL,
  target_open_id TEXT NOT NULL,
  channel        TEXT NOT NULL,               -- feishu_bot / inapp
  event          TEXT NOT NULL,               -- 代理转交 / 退回通知等
  status         TEXT NOT NULL,               -- PENDING/SENT/FAILED
  attempts       INTEGER NOT NULL DEFAULT 0,
  last_error     TEXT,
  sent_at        TEXT,
  created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_notify_log_biz ON t_notify_log(biz_no, event, status);

-- ============ t_instance 增列（04a §1.1；只加列，不改既有列语义） ============
-- 注：SQLite 的 ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS，靠 t_schema_migrations 只执行一次。
ALTER TABLE t_instance ADD COLUMN update_time   INTEGER NOT NULL DEFAULT 0; -- 推送版本号，单调递增（04a §3.1）
ALTER TABLE t_instance ADD COLUMN prev_biz_no   TEXT;                       -- 重新发起时指向旧单号（04a §5.4）
ALTER TABLE t_instance ADD COLUMN cancel_reason TEXT;                       -- 撤回原因
ALTER TABLE t_instance ADD COLUMN cancel_at     TEXT;                       -- 撤回时刻
ALTER TABLE t_instance ADD COLUMN push_hash     TEXT;                       -- 上次推送快照 hash（相同则跳过，04a §3.1）
ALTER TABLE t_instance ADD COLUMN push_at       TEXT;                       -- 上次推送成功时刻

-- ★ 前提 P3：UNIQUE(biz_no) 为「单号永久不复用」的**最终兜底**（04a §1.3 / §6.4）。
--   单实例 + t_doc_seq 单调已保证正常路径不重号；此处拦截「试图复用终态号」这一类
--   异常（命中即报错，**绝不静默分配同号**）。SQLite 唯一索引对 NULL 不生效 →
--   未填单号的存量/事件行不受影响（可多条 NULL）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_instance_biz_no ON t_instance(biz_no);
