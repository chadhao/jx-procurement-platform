-- 0016_attachment_staging.sql —— 提交前附件暂存表（M6 / FR-M0-09 前半 / 决策 D4）。
--
-- ★ 为什么必须独立暂存表：t_attachment（0005）`instance_code NOT NULL` +
--   `UNIQUE(instance_code, file_id)`，且行级权限靠 instance_code 回查 ——
--   提交前的文件**没有实例归属**，不能提前塞 t_attachment。
-- ★ 生命周期：上传即占位（expires_at = +7 天）；提交事务内绑定（bound_biz_no 回填，
--   行迁入 t_attachment）；未绑定过期件由上传时惰性清理（单实例低频，不另起协程）。
-- ★ 只增不改历史。

CREATE TABLE IF NOT EXISTS t_attachment_staging (
  file_id       TEXT    PRIMARY KEY,          -- 我方生成的暂存 id（非飞书 token）
  owner_open_id TEXT    NOT NULL,             -- 仅 owner 可绑定/读取
  file_name     TEXT    NOT NULL,
  size_bytes    INTEGER NOT NULL,
  storage_key   TEXT    NOT NULL,             -- objectstore 键（staging/{open_id}/{file_id}）
  storage_kind  TEXT    NOT NULL DEFAULT '',  -- local / s3（回读校验）
  created_at    TEXT    NOT NULL,
  expires_at    TEXT    NOT NULL,             -- 过期即不可绑定
  bound_biz_no  TEXT                           -- NULL＝未绑定
);

-- 未绑定行按 owner 检索（提交绑定 + owner-only 读取）。
CREATE INDEX IF NOT EXISTS ix_attachment_staging_owner
  ON t_attachment_staging(owner_open_id)
  WHERE bound_biz_no IS NULL;
