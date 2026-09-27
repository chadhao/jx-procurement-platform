-- 0013_callback_repair.sql —— 回调链路修复（docs/16 §2-A/§2-C/§5，第 2 批）配套表演进。
--
-- ★ 两列均**可空、无 NOT NULL、无回填**：
--   ① t_flow_op_log.message_id   —— 飞书回调报文 `message_id`（卡片操作时官方必填），
--      由 recordCallback（internal/flow/callback.go）从 req.MessageID 写入；
--      供第 3 批「失败时调 message/update 更新卡片」使用（docs/16 §2-F 落点，本期只留坑位）。
--   ② t_approval_def.feishu_code —— 三方审批定义「真实 code」候选列（docs/16 G-8 双 code 池）：
--      Registry.Register（internal/approval/defregistry.go）把 `POST external_approvals`
--      响应回填值落此列；主键 approval_code（我方自定义 code）**不变**。
--      ★ 双池归属未实测（docs/16 §7 V-4）⇒ **双写、不猜**：推送侧 Pusher.Push 优先取
--      feishu_code、为空回退 approval_code。
--   ★ 仓库教训 R26：「建了列但没有任何写入者」在 ALTER 路径上门禁（C1）发现不了 ——
--   上述两列的写入者（recordCallback / Registry.Register）与本迁移**必须同批**。
--
-- ★ 幂等：SQLite 的 ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS；
--   靠 t_schema_migrations 保证只执行一次（与 0003/0007/0011 同一约定）。
ALTER TABLE t_flow_op_log ADD COLUMN message_id TEXT;
ALTER TABLE t_approval_def ADD COLUMN feishu_code TEXT;
