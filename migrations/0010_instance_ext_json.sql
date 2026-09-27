-- 0010_instance_ext_json.sql —— 架构转向 ③：t_instance 补 ext_json（承载非规范表单字段）。
--
-- ★ 为什么需要：team-lead 裁决 A5.3 —— `finalize` 的 `ext_json` **来源＝`t_instance.ext_json`**
--   （Submit 时已构造好），finalize 只负责带入台账行并按契约键保留。
--   但 **`t_instance` 原先没有 `ext_json` 列**（旧链把非规范字段写进已弃用的 `t_instance_field`，
--   `ext_json` 只存在于 `t_ledger_archive`）。故此处补列，否则 finalize 无此数据源。
--
-- ★ 分流口径（Submit）：规范字段 → 直接落 t_instance 规范列；**非规范字段 → 汇入本列 `ext_json`**。
--   契约键收敛为 `contract_no`（合同链）/ `related_biz_no`（其他上下游）（决策 #28）。
--
-- ★ 本期只做「增」，不改既有列语义、不删除。
-- ★ 幂等：ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS，靠 t_schema_migrations 只执行一次。

ALTER TABLE t_instance ADD COLUMN ext_json TEXT NOT NULL DEFAULT '{}';
