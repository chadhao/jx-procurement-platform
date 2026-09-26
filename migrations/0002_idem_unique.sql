-- 0002_idem_unique.sql —— 幂等键唯一约束（P0 修复）
--
-- 背景（两条缺陷，缺一不可）：
--
--   ① 幂等键「先查后写」竞态：报送登记的幂等键原存于
--      t_audit_log(action='submission_idem', target_id=<key>)，
--      代码路径为「SELECT 判断是否存在 → 不存在则 INSERT」。
--      这是典型的 TOCTOU：两个并发请求可同时通过判断，各自建一条报送。
--      更糟的是创建失败时已写入的幂等记录不会回滚 → 该键被永久占用，
--      客户端拿同一键重试只会被拒，永远无法成功。
--
--   ② t_submission.biz_no 的 UNIQUE **对 NULL 不生效**：
--      业务单号未填时以 NULL 落库，而 SQLite 的列级 UNIQUE 允许任意多个 NULL
--      （已实测：两条 biz_no=NULL 的行均可插入成功）。
--      即「未填业务单号」时业务唯一键形同虚设。
--
-- 修复：
--   ① 为幂等键建**部分唯一索引**，并把「查后写」改为「先占位、冲突即复用/报错」，
--      以唯一约束冲突替代应用层判断（原子，无竞态）。
--      索引按 (target_id, actor_open_id) 双列：幂等键**按调用方隔离**，
--      避免甲方的键被乙方复用时读到甲方的报送记录（信息泄漏）。
--   ② 以表达式唯一索引把 NULL 也纳入约束 —— COALESCE(biz_no,'') 使所有
--      空业务单号映射到同一键，从而**至多允许一条**无业务单号的报送（硬兜底）。
--      应用层同时强制 biz_no 必填（400），索引仅作最后一道防线。
--
-- ★ 索引创建会因已存在重复数据而失败（这是期望行为：宁可启动失败也不留脏数据）。
--   本系统尚未投产，dev 库如需重来可直接删除 ./data/jxapproval.db。

CREATE UNIQUE INDEX IF NOT EXISTS ux_audit_idem_key
  ON t_audit_log(target_id, actor_open_id)
  WHERE action = 'submission_idem';

CREATE UNIQUE INDEX IF NOT EXISTS ux_submission_biz_no_nonnull
  ON t_submission(COALESCE(biz_no, ''));
