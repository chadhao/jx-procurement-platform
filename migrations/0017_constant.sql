-- 0017_constant.sql —— 运营性常量表（T2 / N-025 / R-24 用户 A4 定案）。
--
-- ★ 三分法（spec/constants.json#three_way_split）：本表只存**运营性常量**
--   （unit / role_display_name / contract_template）—— 改了不影响流程线与硬校验。
-- ★ 纪律（policy）：
--   - **只停用（retired），不物理删除** —— 历史单据存值快照，删掉会让「值从哪来」无从追溯；
--   - 每次增删改写审计（操作人/时间/表/条目前后值）—— 由 API 层写 t_audit_log；
--   - role_display_name **只能改显示名，禁止增删角色** —— API 层强校验（角色有无归 chain.json）。
-- ★ 快照规则：单据保存时同时存 key 与显示值快照（submit 侧写 <字段>_snapshot 进 ext_json），
--   字典可变、已落单据取值冻结。
-- ★ 只增不改历史。

CREATE TABLE IF NOT EXISTS t_constant (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  table_key  TEXT    NOT NULL,                -- unit / role_display_name / contract_template
  value      TEXT    NOT NULL,                -- 显示值（可改名；历史单据不跟随）
  sort_order INTEGER NOT NULL DEFAULT 0,
  status     TEXT    NOT NULL DEFAULT 'active', -- active | retired（★ 无物理删除路径）
  created_at TEXT    NOT NULL,
  updated_at TEXT    NOT NULL,
  UNIQUE(table_key, value)
);

CREATE INDEX IF NOT EXISTS ix_constant_table ON t_constant(table_key, status, sort_order);
