-- =============================================================================
-- 采购与费用审批平台（自建侧）· 初始表结构（20 张表）
-- 依据 docs/04-Architecture.md §3.2 DDL 草案落地。
-- 说明：
--   1) 未定口径（approval_code / 字段 id / 权限矩阵）一律由配置表承接，不进入硬编码列。
--   2) 设计文档中 t_config_mapping 的 UNIQUE 含表达式（COALESCE），SQLite 不允许在
--      表级 UNIQUE 约束内使用表达式，故改为唯一索引（见 06-Implementation-Notes.md 冲突 #1）。
-- =============================================================================

-- ============ M2 实例主表 ============
CREATE TABLE IF NOT EXISTS t_instance (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  instance_code     TEXT    NOT NULL UNIQUE,           -- 飞书实例 ID
  approval_code     TEXT    NOT NULL,                  -- 映射到单据类型（经 t_config_mapping）
  doc_type          TEXT,                              -- BA/PR/SA/RFQ/BJ/SS/CT/PC/GR/QC/SUB（映射得出）
  biz_no            TEXT,                              -- 业务单号：前缀-YYMM-####（飞书流水号控件生成，自建侧只读）
  biz_no_prefix     TEXT,                              -- 归档前缀
  biz_no_yymm       TEXT,                              -- YYMM（可空，Q7 待确认是否按月重置）
  biz_no_seq        TEXT,                              -- ####（只读归档，自建侧不生成）
  status            TEXT    NOT NULL,                  -- 状态机收敛后的当前状态
  status_raw        TEXT,                              -- 飞书原始状态（保真）
  applicant_open_id TEXT,                              -- 申请人
  applicant_name    TEXT,
  department        TEXT,                              -- 部门（行级过滤依据）
  amount_cents      INTEGER,                           -- 金额（分；列级权限保护对象）
  purpose_class_l1  TEXT,                              -- 用途一级分类
  purpose_class_l2  TEXT,                              -- 用途二级明细
  supplier          TEXT,                              -- 供应商（防拆分累计依据）
  source            TEXT NOT NULL DEFAULT 'event',     -- event / reconcile（区分事件入库 or 对账补录）
  created_at        TEXT NOT NULL,                     -- ISO8601 UTC
  updated_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_instance_approval_code ON t_instance(approval_code, created_at);
CREATE INDEX IF NOT EXISTS idx_instance_dept          ON t_instance(department);
CREATE INDEX IF NOT EXISTS idx_instance_applicant     ON t_instance(applicant_open_id);
CREATE INDEX IF NOT EXISTS idx_instance_supplier      ON t_instance(supplier, biz_no_yymm);
CREATE INDEX IF NOT EXISTS idx_instance_biz_no        ON t_instance(biz_no);

-- ============ M2 实例表单字段（键值对，不硬编码字段顺序） ============
CREATE TABLE IF NOT EXISTS t_instance_field (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  instance_code  TEXT    NOT NULL,
  field_id       TEXT    NOT NULL,                    -- 飞书表单控件 id（原样存，映射到业务名走 t_config_mapping）
  field_name     TEXT,                                -- 展示名（模板可能变名，仅作展示）
  biz_field      TEXT,                                -- 经映射表得到的业务字段名（可空表示未映射，Q1）
  value_text     TEXT,                                -- 统一以文本存储，按需解析
  value_type     TEXT,                                -- text/number/date/attachment/option...
  raw_json       TEXT,                                -- 原始片段（保真，应对控件演进）
  created_at     TEXT NOT NULL,
  UNIQUE(instance_code, field_id)
);
CREATE INDEX IF NOT EXISTS idx_field_biz ON t_instance_field(instance_code, biz_field);

-- ============ M2/M7 状态变更史（驳回重提的链式留痕，追加式） ============
CREATE TABLE IF NOT EXISTS t_instance_status_history (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  instance_code    TEXT    NOT NULL,
  status           TEXT    NOT NULL,
  task_node        TEXT,                              -- 节点名（approval_task 事件）
  operator_open_id TEXT,
  opinion          TEXT,                              -- 审批意见（驳回原因等）
  occurred_at      TEXT    NOT NULL,                  -- 事件发生时间
  event_seq        INTEGER NOT NULL,                  -- 同实例内递增序号（幂等增强，见架构 §4.3）
  created_at       TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_hist_instance ON t_instance_status_history(instance_code, event_seq);

-- ============ M3 事件收件箱（幂等） ============
CREATE TABLE IF NOT EXISTS t_event_inbox (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  idem_key       TEXT    NOT NULL,                    -- 幂等键 = 事件级唯一 ID（2.0 header.event_id / 1.0 uuid）
  event_type     TEXT    NOT NULL,                    -- approval_instance / approval_task
  instance_code  TEXT    NOT NULL,
  status         TEXT,
  event_id       TEXT,                                -- 飞书事件/消息 ID（若可用，仅作辅助）
  payload        TEXT    NOT NULL,                    -- 原始事件报文（保真）
  process_state  TEXT    NOT NULL DEFAULT 'PENDING',  -- PENDING/PROCESSING/DONE/FAILED/DEAD
  retry_count    INTEGER NOT NULL DEFAULT 0,
  last_error     TEXT,
  received_at    TEXT    NOT NULL,                    -- 入库时刻（用于 3 秒路径耗时观测）
  processed_at   TEXT,
  UNIQUE(idem_key)                                    -- ★ 幂等唯一约束：重复事件在此处被拦
);
CREATE INDEX IF NOT EXISTS idx_inbox_state ON t_event_inbox(process_state, received_at);

-- ============ M3 异步作业 / 重试 ============
CREATE TABLE IF NOT EXISTS t_worker_job (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  inbox_id       INTEGER NOT NULL,
  job_type       TEXT    NOT NULL,                    -- fetch_detail / parse_field / write_ledger
  state          TEXT    NOT NULL DEFAULT 'QUEUED',
  attempts       INTEGER NOT NULL DEFAULT 0,
  next_run_at    TEXT,
  last_error     TEXT,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  UNIQUE(inbox_id, job_type)                          -- 同一 inbox 同一作业不重复
);
CREATE INDEX IF NOT EXISTS idx_job_sched ON t_worker_job(state, next_run_at);

-- ============ M3 死信 ============
CREATE TABLE IF NOT EXISTS t_deadletter (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  inbox_id         INTEGER NOT NULL,
  reason           TEXT    NOT NULL,
  payload          TEXT,
  replay_count     INTEGER NOT NULL DEFAULT 0,
  created_at       TEXT NOT NULL,
  last_replayed_at TEXT
);

-- ============ M0 同步游标 ============
CREATE TABLE IF NOT EXISTS t_sync_cursor (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  approval_code  TEXT    NOT NULL,
  cursor_kind    TEXT    NOT NULL,                    -- reconcile（批量取实例 ID 的时间窗游标）
  last_synced_at TEXT,                                -- 上次同步起点（时间窗）
  last_run_at    TEXT,
  last_missing   INTEGER DEFAULT 0,                   -- 上次对账求差缺失条数
  last_filled    INTEGER DEFAULT 0,                   -- 上次补录条数
  UNIQUE(approval_code, cursor_kind)
);

-- ============ M0 订阅状态（先订阅 + 健康检查） ============
CREATE TABLE IF NOT EXISTS t_subscribe_state (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  approval_code   TEXT    NOT NULL UNIQUE,
  doc_type        TEXT,
  subscribed      INTEGER NOT NULL DEFAULT 0,          -- 0/1
  last_result     TEXT,                                -- 成功/失败码
  last_error      TEXT,
  last_attempt_at TEXT,
  updated_at      TEXT NOT NULL
);

-- ============ M0/M2 配置映射（★ 配置化，不硬编码） ============
CREATE TABLE IF NOT EXISTS t_config_mapping (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  map_kind       TEXT    NOT NULL,                    -- 'approval_code' | 'field_id' | 'threshold' | 'enum'
  map_key        TEXT    NOT NULL,                    -- approval_code / 控件 id / 枚举键
  map_value      TEXT    NOT NULL,                    -- 单据类型 / 业务字段名 / 阈值等
  doc_type       TEXT,                                -- 作用于哪类单据（field_id 映射用）
  remark         TEXT,
  updated_at     TEXT NOT NULL
);
-- ★ SQLite 不支持表级 UNIQUE 含表达式，改用唯一索引（原设计：UNIQUE(map_kind,map_key,COALESCE(doc_type,''))）
CREATE UNIQUE INDEX IF NOT EXISTS ux_config_mapping ON t_config_mapping(map_kind, map_key, COALESCE(doc_type, ''));

-- ============ M5 用户角色映射 ============
CREATE TABLE IF NOT EXISTS t_user_role (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  open_id        TEXT    NOT NULL UNIQUE,             -- 飞书 open_id
  name           TEXT,
  role           TEXT    NOT NULL,                    -- 申请人/主管领导/项目总经理/副总/综合运营主管/采购经办人/验收人/系统管理员
  department     TEXT,                                -- 主部门
  extra_depts    TEXT,                                -- 分管部门（JSON 数组，供行级范围令牌）
  active         INTEGER NOT NULL DEFAULT 1,
  updated_at     TEXT NOT NULL
  -- ★ 未在此表映射的 open_id → 默认拒绝（deny by default），对应 UC-01/A2、TC-32
);

-- ============ M5 权限规则（配置驱动，口径待 Q3 定案） ============
CREATE TABLE IF NOT EXISTS t_permission_rule (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  resource        TEXT    NOT NULL,                    -- ledger:采购备案台账 / dashboard:14 / api:instances 等
  role            TEXT    NOT NULL,
  row_scope       TEXT    NOT NULL,                    -- SELF/DEPT/CHARGE_DEPT/ALL/ASSIGNED/PARTICIPATED
  column_allow    TEXT,                                -- 允许列（JSON 数组；NULL = 全部允许）
  column_deny     TEXT,                                -- 拒绝列（JSON 数组；优先级高于 column_allow）
  writable_fields TEXT,                                -- 可写字段（JSON 数组；仅运营表有效）
  effective_from  TEXT,                                -- 生效时间（口径变更留痕）
  remark          TEXT,
  updated_at      TEXT NOT NULL,
  UNIQUE(resource, role)
  -- ★ 此表内容默认取 PRD §4.2「建议值」；Q3 定案后仅改数据行，不改代码
);

-- ============ M4 台账·同步存档（只读，审批自动写入） ============
-- 建模策略：单表 + 类型字段（取舍见架构 §3.3）。核心列为稳定字段，变动字段入 ext_json。
CREATE TABLE IF NOT EXISTS t_ledger_archive (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  ledger_type       TEXT    NOT NULL,                  -- 台账类型（L01..L12 语义键，见架构 §3.4）
  biz_no            TEXT,                              -- 业务单号（贯穿全链）
  instance_code     TEXT,                              -- 来源实例
  source_doc_type   TEXT,
  department        TEXT,
  applicant_open_id TEXT,
  submitter_open_id TEXT,
  amount_cents      INTEGER,
  supplier          TEXT,
  purpose_class_l1  TEXT,
  purpose_class_l2  TEXT,
  biz_date          TEXT,                              -- 业务日期（YYMM 解析等）
  ext_json          TEXT    NOT NULL DEFAULT '{}',     -- 类型专属字段（键值对，不硬编码列）
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  UNIQUE(ledger_type, biz_no)                          -- 同类型同业务单号唯一（幂等写入）
);
CREATE INDEX IF NOT EXISTS idx_arch_type_dept        ON t_ledger_archive(ledger_type, department);
CREATE INDEX IF NOT EXISTS idx_arch_supplier_month   ON t_ledger_archive(ledger_type, supplier, biz_date);

-- ============ M4 台账·运营表（可写，仅运营字段） ============
CREATE TABLE IF NOT EXISTS t_ledger_ops (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  ledger_type  TEXT    NOT NULL,
  biz_no       TEXT    NOT NULL,                       -- 以业务单号关联存档表（不做二次录入）
  ops_json     TEXT    NOT NULL DEFAULT '{}',          -- 审批后字段（付款凭据号/抽查状态/经办状态/完成日期/...(Q14)）
  updated_by   TEXT,
  updated_at   TEXT NOT NULL,
  UNIQUE(ledger_type, biz_no)
);

-- ============ M4 台账字段定义（可见/可写/公式） ============
CREATE TABLE IF NOT EXISTS t_ledger_field_def (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  ledger_type  TEXT    NOT NULL,
  field_key    TEXT    NOT NULL,                       -- 对应 archive.ext_json / ops_json 的键
  field_label  TEXT,
  is_formula   INTEGER NOT NULL DEFAULT 0,             -- 公式列红标（FR-M4-04）
  formula_kind TEXT,                                   -- same_person / supplier_month_sum / spot_check_range ...
  is_sensitive INTEGER NOT NULL DEFAULT 0,             -- 是否敏感列（金额类，供列级权限兜底）
  UNIQUE(ledger_type, field_key)
);

-- ============ M1 备付金签领登记 ============
CREATE TABLE IF NOT EXISTS t_petty_cash_receipt (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  biz_no           TEXT,                               -- 关联采购报备单（BA）
  instance_code    TEXT,
  receiver_open_id TEXT,
  receiver_name    TEXT,
  amount_cents     INTEGER NOT NULL,
  received_date    TEXT    NOT NULL,
  created_by       TEXT,
  created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pc_receipt_biz ON t_petty_cash_receipt(biz_no);

-- ============ M1 备付金月核销 ============
CREATE TABLE IF NOT EXISTS t_petty_cash_close (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  period        TEXT    NOT NULL,                      -- 账期 YYYY-MM
  issued_cents  INTEGER,                               -- 当期签领
  spent_cents   INTEGER,                               -- 当期支出
  balance_cents INTEGER,                               -- 核销后余额（唯一可外部核对的锚点）
  remark        TEXT,
  created_by    TEXT,
  created_at    TEXT NOT NULL,
  UNIQUE(period)
);

-- ============ M1 集团报销跟踪表（人工登记） ============
CREATE TABLE IF NOT EXISTS t_expense_track (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  src_biz_no        TEXT,                               -- 关联事前申请单号（SA）
  applicant_open_id TEXT,
  department        TEXT,
  actual_cents      INTEGER,
  invoice_count     INTEGER,
  review_state      TEXT,                               -- 初审状态
  handover_date     TEXT,                               -- 移交集团日期
  paid_date         TEXT,                               -- 集团付款日期（人工）
  paid_cents        INTEGER,
  overrun_note      TEXT,                               -- 超支说明
  created_by        TEXT,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);

-- ============ M6 报送登记 ============
CREATE TABLE IF NOT EXISTS t_submission (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  biz_no         TEXT UNIQUE,                          -- SUB-YYMM-####
  subject_type   TEXT,                                 -- 事项类型
  amount_cents   INTEGER,
  pay_method     TEXT,
  hn_finish_date TEXT,                                 -- 湖南侧完成日期
  submit_date    TEXT,                                 -- 提交集团日期
  receipt_ref    TEXT,                                 -- 移交凭证（签收记录）★ 为空则视为未提交
  submit_state   TEXT    NOT NULL,                     -- 未提交/已提交/办理中/已付款/已驳回
  grp_accept_no  TEXT,                                 -- 集团受理编号（人工）
  grp_state      TEXT,                                 -- 集团流程状态（人工）
  paid_date      TEXT,                                 -- 付款完成日期（人工）
  reject_reason  TEXT,                                 -- 驳回原因与处置
  created_by     TEXT,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  CHECK (receipt_ref IS NULL OR receipt_ref <> '' OR submit_state <> '已提交')
);
CREATE INDEX IF NOT EXISTS idx_sub_state ON t_submission(submit_state, hn_finish_date);

-- ============ M6 报送关联单据 ============
CREATE TABLE IF NOT EXISTS t_submission_item (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  submission_id INTEGER NOT NULL,
  item_biz_no   TEXT    NOT NULL,                      -- PR / CT / GR / 发票 / 比价表等
  item_type     TEXT,
  FOREIGN KEY (submission_id) REFERENCES t_submission(id) ON DELETE CASCADE
);

-- ============ M7 审计日志 ============
CREATE TABLE IF NOT EXISTS t_audit_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_open_id TEXT,
  actor_role    TEXT,
  action        TEXT    NOT NULL,                      -- view/query/update/export/login/denied/replay/subscribe...
  resource      TEXT,                                  -- 台账/看板/实例/凭证包
  target_id     TEXT,
  result        TEXT,                                  -- allow / deny
  detail_json   TEXT,
  feishu_log_id TEXT,                                  -- ★ 飞书调用 log_id（排障用）
  ip            TEXT,
  created_at    TEXT NOT NULL                          -- ISO8601 UTC
);
CREATE INDEX IF NOT EXISTS idx_audit_time  ON t_audit_log(created_at);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON t_audit_log(actor_open_id, action);
