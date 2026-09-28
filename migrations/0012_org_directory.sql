-- 0012_org_directory.sql —— 飞书通讯录镜像（docs/08-Org-Sync-Design.md §4.2，V1.8 定稿）。
--
-- ★ 编号依据：原设计号 0007 撞 0007_approval_core.sql 后改 0012（docs/08 §4.2「编号变更」）；
--   git 历史核实 0012 从未被占用（0011 → 0013 跳号即为留给本迁移的空位）。
--
-- ★★ 红线（docs/08 §4.10「镜像 ≠ 权限」）：
--   · 四张表均为**只读镜像/元信息**，不含任何「准入/角色」列（无 role / active / row_scope）；
--   · 与权限表 t_user_role **物理分离、无外键**；同步单向（飞书 → 镜像），绝不反向写权限。
--   · 软删只置 is_deleted=1，**永不物理删除**（口径④「只增不减」）；软删行仍参与历史名称解析。
--
-- ★ ID 口径（docs/08 §5-C-A 路线甲）：open_department_id（od- 前缀，系统生成、不可编辑）
--   为权威主键；department_id（可自定义、可被改）作桥接二级键，每次全量刷新。
--   根部门 open_department_id = "0"。
-- 时间列沿用仓库约定：ISO8601 UTC 文本（RFC3339）。

-- ---------- 部门镜像 ----------
CREATE TABLE IF NOT EXISTS t_org_department (
  open_department_id       TEXT PRIMARY KEY,          -- od- 前缀；根部门 = '0'
  department_id            TEXT NOT NULL DEFAULT '',  -- 可变自定义 ID（桥接键；每次全量刷新）
  parent_open_department_id TEXT NOT NULL DEFAULT '', -- 上级部门；根为 '0'
  name                     TEXT NOT NULL DEFAULT '',  -- 部门名称（来自飞书；字段权限缺失时可能为空 → 计入 field_gaps）
  name_path                TEXT NOT NULL DEFAULT '',  -- 全路径（A/B/C，本地派生，供展示/检索）
  is_deleted               INTEGER NOT NULL DEFAULT 0,-- 软删标记（0 在用 / 1 已删）
  raw_json                 TEXT NOT NULL DEFAULT '{}',-- 原始报文保真
  first_seen_at            TEXT NOT NULL,
  last_seen_at             TEXT NOT NULL,
  updated_at               TEXT NOT NULL,
  source                   TEXT NOT NULL DEFAULT ''   -- full / manual / event
);

CREATE INDEX IF NOT EXISTS idx_org_dept_custom_id ON t_org_department(department_id);
CREATE INDEX IF NOT EXISTS idx_org_dept_parent    ON t_org_department(parent_open_department_id);
CREATE INDEX IF NOT EXISTS idx_org_dept_deleted   ON t_org_department(is_deleted);

-- ---------- 人员镜像 ----------
CREATE TABLE IF NOT EXISTS t_org_user (
  open_id               TEXT PRIMARY KEY,
  union_id              TEXT NOT NULL DEFAULT '',
  user_id               TEXT NOT NULL DEFAULT '',
  name                  TEXT NOT NULL DEFAULT '',   -- 姓名（字段权限缺失时可能为空 → 计入 field_gaps）
  employee_status       TEXT NOT NULL DEFAULT '',   -- 归一在职态：在职/离职/冻结/未激活/未入职
  is_resigned           INTEGER NOT NULL DEFAULT 0,
  is_exited             INTEGER NOT NULL DEFAULT 0,
  is_frozen             INTEGER NOT NULL DEFAULT 0,
  is_activated          INTEGER NOT NULL DEFAULT 0,
  is_unjoin             INTEGER NOT NULL DEFAULT 0,
  primary_department_id TEXT NOT NULL DEFAULT '',   -- 主部门 open_department_id（department_ids 首项）
  department_ids        TEXT NOT NULL DEFAULT '[]', -- 关系数组 JSON（元素为 od- 前缀 open_department_id）
  is_deleted            INTEGER NOT NULL DEFAULT 0, -- 软删：删除事件 / 全量缺失 / 离职统一置 1
  raw_json              TEXT NOT NULL DEFAULT '{}',
  first_seen_at         TEXT NOT NULL,
  last_seen_at          TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  source                TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_org_user_primary_dept ON t_org_user(primary_department_id);
CREATE INDEX IF NOT EXISTS idx_org_user_deleted      ON t_org_user(is_deleted);

-- ---------- 同步状态（单行表） ----------
CREATE TABLE IF NOT EXISTS t_org_sync_state (
  id                   INTEGER PRIMARY KEY CHECK(id = 1),
  last_full_attempt_at TEXT NOT NULL DEFAULT '',
  last_full_success_at TEXT NOT NULL DEFAULT '',   -- 启动阈值判据（失败不推进）
  last_full_error      TEXT NOT NULL DEFAULT '',
  last_full_dept_count INTEGER NOT NULL DEFAULT 0,
  last_full_user_count INTEGER NOT NULL DEFAULT 0,
  last_event_at        TEXT NOT NULL DEFAULT '',
  updated_at           TEXT NOT NULL
);

-- ---------- 全量运行流水（追加式；供差异报告可见） ----------
CREATE TABLE IF NOT EXISTS t_org_sync_run (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  run_at             TEXT NOT NULL,
  trigger            TEXT NOT NULL,                 -- startup / weekly / manual
  result             TEXT NOT NULL,                 -- ok / failed
  dept_added         INTEGER NOT NULL DEFAULT 0,
  dept_updated       INTEGER NOT NULL DEFAULT 0,
  dept_soft_deleted  INTEGER NOT NULL DEFAULT 0,
  user_added         INTEGER NOT NULL DEFAULT 0,
  user_updated       INTEGER NOT NULL DEFAULT 0,
  user_soft_deleted  INTEGER NOT NULL DEFAULT 0,
  field_gaps_json    TEXT NOT NULL DEFAULT '{}',    -- 字段覆盖缺口（静默防护，docs/08 §4.11-C）
  error              TEXT NOT NULL DEFAULT '',
  duration_ms        INTEGER NOT NULL DEFAULT 0
);
