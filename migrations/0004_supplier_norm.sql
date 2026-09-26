-- 0004: 供应商名称归一分组键（PRD Q20 定案 2026-09-27）
--
-- 背景：制度第二十一条第 7 项的「同供应商当月累计 ≥1,000 元」是采一档（唯一免审批档）
-- 防拆分的核心抓手。若按**原样字符串**分组，同一家供应商换个写法（多打空格、全角空格、
-- 大小写不同）即可让累计永远达不到阈值 —— 防拆分指标被**静默规避**。
--
-- 做法：新增 `supplier_norm` 作为**分组/比较键**（由 Go 侧 normalize.Supplier 统一写入，
-- 写入点收在 store.upsertArchive 这一处，避免"列加了但没人写"）；
-- **展示仍用原名 `supplier`**。
--
-- 注意：SQLite 的 UPPER()/REPLACE() 只能处理 ASCII，**无法做全角→半角**，
-- 故下面这段回填是**尽力而为**（只处理空白与大小写），只对历史存量有意义；
-- 本系统尚未上线，实际存量极少，新写入一律由 Go 侧完整归一。

ALTER TABLE t_ledger_archive ADD COLUMN supplier_norm TEXT;

CREATE INDEX IF NOT EXISTS idx_archive_supplier_norm ON t_ledger_archive(supplier_norm);

UPDATE t_ledger_archive
SET supplier_norm = UPPER(REPLACE(REPLACE(REPLACE(TRIM(supplier), ' ', ''), char(12288), ''), char(160), ''))
WHERE COALESCE(supplier, '') <> '' AND COALESCE(supplier_norm, '') = '';
