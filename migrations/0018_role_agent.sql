-- 0018_role_agent.sql —— 角色代理人配置表（N-028 / spec/authority.json · 制度第十二条）。
--
-- ★ 授权配置（第四类，非运营性常量）：它决定「谁能审」—— 有显式约束兜底：
--   - 每 role_key **至多 1 条 active**（部分唯一索引，与并发模型无关的最终兜底）；
--   - 只停用（state=retired），不物理删除（在办单据的代理关系须可追溯）；
--   - agent_open_id 须为通讯录镜像内**未离职**用户（API 层硬校验，见 handlers）；
--   - ★ 代理人**不参与审批人解析**（用户定案「不需要替补」）——本表当前唯一消费端是
--     **负向守卫测试**；正向消费端（转交/回退按代理人）属 M9，feature_enabled=false。
-- ★ 只增不改历史。

CREATE TABLE IF NOT EXISTS t_role_agent (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  role_key       TEXT    NOT NULL,              -- spec/chain.json#roles 键，且 ∈ agent_eligible_roles.eligible
  agent_open_id  TEXT    NOT NULL,              -- 代理人（t_user / 镜像 open_id）
  state          TEXT    NOT NULL DEFAULT 'active', -- active | retired（★ 无物理删除路径）
  note           TEXT    NOT NULL DEFAULT '',   -- 备注（「张三出差期间」——事后判断当时是否合理）
  created_by     TEXT    NOT NULL DEFAULT '',   -- 登记人（★ 系统不校验其是否为主管领导 —— designator.system_scope）
  created_at     TEXT    NOT NULL,
  updated_by     TEXT    NOT NULL DEFAULT '',
  updated_at     TEXT    NOT NULL
);

-- ★ 每角色至多 1 名**生效**代理人（checks#single_active_agent_per_role）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_role_agent_active
  ON t_role_agent(role_key)
  WHERE state = 'active';

CREATE INDEX IF NOT EXISTS ix_role_agent_state ON t_role_agent(state, role_key);
