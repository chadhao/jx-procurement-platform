-- 0005: 附件元数据表（B39 最小集：**只做下载**，不做上传）
--
-- 背景（架构审查 B39）：FR-M0-09 要求附件可下载并落主存；模式 A 下本系统**不创建实例**，
-- 附件由申请人在飞书侧上传 → 本系统只需「按 file_id 取回并缓存」+「凭证包能取到文件」。
--
-- 设计要点：
--  ① **元数据在入库时登记**（只 INSERT，不做网络 IO）—— 事件处理有 3 秒窗口，绝不能在此下载；
--  ② 文件本体**按需拉取**（用户点下载 / 生成凭证包时），拉到后写对象存储并回填 storage_key；
--  ③ 行级权限**不冗余存身份列**，一律以 `instance_code` 回查 `t_instance` 的可见性，
--     避免"两处身份列不同步"的老问题（与 0003 的教训相反：那里是必须冗余，这里是必须不冗余）。
CREATE TABLE IF NOT EXISTS t_attachment (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  file_id         TEXT    NOT NULL,               -- 飞书 file_token
  instance_code   TEXT    NOT NULL,               -- 归属实例（权限经 t_instance 判定）
  biz_no          TEXT,                           -- 业务单号（可空）
  field_id        TEXT,                           -- 来源控件 id
  file_name       TEXT,
  size_bytes      INTEGER,
  storage_key     TEXT,                           -- 已落主存的对象键；NULL＝尚未拉取
  storage_kind    TEXT,                           -- local / s3
  fetched_at      TEXT,                           -- 最近一次成功拉取时间
  created_at      TEXT    NOT NULL,
  updated_at      TEXT    NOT NULL,
  UNIQUE(instance_code, file_id)
);

CREATE INDEX IF NOT EXISTS idx_attachment_file  ON t_attachment(file_id);
CREATE INDEX IF NOT EXISTS idx_attachment_inst  ON t_attachment(instance_code);
