-- 0006_ledger_multi.sql
-- B47 修复：`doc_type → ledger_type` 由**一对一**放宽为**一对多**。
--
-- 起因：工具表·台账与看板设计 第 3 号「采购经办登记台账 L03」明确数据来源为
--       「物资采购申请单审批节点指定经办人后自动写入」，但原唯一索引
--       `ux_config_mapping(map_kind, map_key, COALESCE(doc_type,''))` 使
--       同一 doc_type 只能配一个台账 → `PR` 指了 `L02` 之后，`L03` **无人写入**。
--       连带后果：看板「需求提出人任经办人的笔数」读 `L03` → **恒为 0，而 0 恰是期望值**
--       （「没有违规」与「没有数据」界面无法区分）。
--
-- 改法：把 `map_value` 纳入唯一键 → 同一 (map_kind, map_key, doc_type) 下允许出现多条，
--       只要 `map_value` 不同。对 `ledger_type` 即：(doc_type, ledger_type) 唯一。
--
-- ★ 为什么对其他映射类不构成放松（唯一性在**应用层**更严地兜住）：
--   - approval_code：`Validate()` 保证 code 全局唯一（DB 层不再兜，但导入是唯一写入口）
--   - field_id     ：`Validate()` 保证 (doc_type, field_id) 唯一 → 一个控件仍只映射一个 biz_field
--   - threshold    ：`Validate()` 保证 key 唯一 → 一个阈值键仍只能有一个值
--   - ledger_type  ：★ 本轮**放宽**为 (doc_type, ledger_type) 唯一（这正是修复目标）
--
-- ★ 新索引严格弱于旧索引（多了 map_value 列）→ 在既有数据上**不可能**建失败。

DROP INDEX IF EXISTS ux_config_mapping;

CREATE UNIQUE INDEX IF NOT EXISTS ux_config_mapping
  ON t_config_mapping(map_kind, map_key, COALESCE(doc_type, ''), map_value);
