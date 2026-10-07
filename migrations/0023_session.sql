-- 0023: N-077 —— 会话落库 t_session（原会话存内存 map ⇒ 服务重启全部失效，
-- 用户被迫反复飞书授权）。
--
-- ★ 与内存同构：access.Store 仅换后端，Establish/Resolve/Destroy 语义不变。
-- ★ 规模：单实例 5–10 人协作，会话表数十行；expires_at 索引供过期清理。
-- ★ 幂等（IF NOT EXISTS；与仓库其余迁移同款）。
-- ★ 时间列 TEXT（RFC3339 UTC，与 t_user_role.updated_at 同形态）。

CREATE TABLE IF NOT EXISTS t_session (
  id         TEXT PRIMARY KEY,             -- 会话 id（cookie = id.HMAC 签名，见 access.Store.sign）
  open_id    TEXT    NOT NULL,             -- 飞书 open_id（角色不入会话，TC-11）
  issued_at  TEXT    NOT NULL,             -- 签发时刻
  expires_at TEXT    NOT NULL,             -- 过期时刻（滑动续期：Resolve 通过且过节流窗 ⇒ 刷新为 now+ttl）
  updated_at TEXT    NOT NULL              -- 最近一次续期/写入时刻（节流判定基准）
);

CREATE INDEX IF NOT EXISTS idx_session_expires ON t_session(expires_at);
