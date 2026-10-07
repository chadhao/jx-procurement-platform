-- 0022: N-075 —— 系统角色独立成表 t_sys_role（解 t_user_role.open_id UNIQUE 与
-- 「一人多类角色」的冲突：同一 open_id 既是审批角色〔项目总经理〕又是系统角色
-- 〔系统管理员〕时，二者互斥无法共存）。
--
-- ★ 编号说明：N-075 规格原文写 `0021_sys_role.sql`，但 0021 已被
--   `0021_l01_written_off_balance.sql`（N-061 T1）占用 ⇒ 迁移按文件名升序顺延为 0022。
-- ★ UNIQUE(open_id, role)（规格 Q1 推荐）：系统角色可叠加、与 t_user_role 的
--   open_id UNIQUE 本质解耦；idx_sys_role_openid 供身份解析（GetSysRoles）点查。
-- ★ 本文件与仓库其余迁移同款**幂等**写法（Migrate 对已应用文件跳过；SQL 自身亦可重跑）。

CREATE TABLE IF NOT EXISTS t_sys_role (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  open_id    TEXT    NOT NULL,                       -- 飞书 open_id
  role       TEXT    NOT NULL,                       -- 系统角色枚举（当前仅：系统管理员）
  active     INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT    NOT NULL,
  UNIQUE(open_id, role)
);

CREATE INDEX IF NOT EXISTS idx_sys_role_openid ON t_sys_role(open_id);

-- 存量迁移：t_user_role 中 role='系统管理员' 的行迁入 t_sys_role 并从 t_user_role 删除
-- （否则其 open_id 仍占 UNIQUE、问题不解决）。
-- 幂等：INSERT OR IGNORE（已存在即跳过）＋ DELETE（无匹配行即无操作）；重复执行结果一致。
INSERT OR IGNORE INTO t_sys_role (open_id, role, active, updated_at)
SELECT open_id, role, active, updated_at FROM t_user_role WHERE role = '系统管理员';

DELETE FROM t_user_role WHERE role = '系统管理员';
