package dashboard

// N-078 Q3（制度『防拆分』月度报送清单）：看板 16 第 14 指标
// low_value_high_freq_supplier（1,000 元以下高频供应商 · 当月 ≥3 笔）。
//
// 本文件两条用例分别钉住两件**独立**的事（便于单点变异时「恰红、不误伤」）：
//   - ✅ 边界：入列笔数下限 ≥3（2 笔不入、3 笔入）＋ 采一档金额范围（<1,000 元）＋ 同月口径；
//   - ✅ 口径同源：分组键与 split_suspicion **逐字一致**（同一 supplierGroupKey，非各写一份字面量）。

import "testing"

// TestLowValueHighFreqSupplierBoundary 入列边界（≥3 笔）＋ 采一档范围 ＋ 同月口径。
// ★ 关键：把「≥3」改成「≥4」⇒ 本用例**恰红**（乙 3 笔会被排除）；其余用例不受影响。
func TestLowValueHighFreqSupplierBoundary(t *testing.T) {
	b := &Builder{}
	rows := []Row{
		// 甲：当月 2 笔（<3）⇒ 不入列（正是 ≥3 的下界反例）
		{Supplier: "供应商甲", SupplierNorm: "供应商甲", BizNo: "A1", BizDate: "2026-09-05", AmountCents: 30000},
		{Supplier: "供应商甲", SupplierNorm: "供应商甲", BizNo: "A2", BizDate: "2026-09-20", AmountCents: 40000},
		// 乙：当月 3 笔、均 <1,000 元 ⇒ 入列（count=3 / sum=60000）
		{Supplier: "供应商乙", SupplierNorm: "供应商乙", BizNo: "B1", BizDate: "2026-09-01", AmountCents: 10000},
		{Supplier: "供应商乙", SupplierNorm: "供应商乙", BizNo: "B2", BizDate: "2026-09-02", AmountCents: 20000},
		{Supplier: "供应商乙", SupplierNorm: "供应商乙", BizNo: "B3", BizDate: "2026-09-03", AmountCents: 30000},
		// 丙：单笔但 ≥1,000 元 ⇒ 不属采一档 ⇒ 不入列（采一档范围反例）
		{Supplier: "供应商丙", SupplierNorm: "供应商丙", BizNo: "C1", BizDate: "2026-09-01", AmountCents: 200000},
		// 丁：3 笔里 1 笔 ≥1,000 元 ⇒ 采一档仅 2 笔 ⇒ 不入列（范围与笔数共同生效）
		{Supplier: "供应商丁", SupplierNorm: "供应商丁", BizNo: "D1", BizDate: "2026-09-01", AmountCents: 30000},
		{Supplier: "供应商丁", SupplierNorm: "供应商丁", BizNo: "D2", BizDate: "2026-09-02", AmountCents: 30000},
		{Supplier: "供应商丁", SupplierNorm: "供应商丁", BizNo: "D3", BizDate: "2026-09-03", AmountCents: 120000},
		// 戊：跨月（08 月 1 笔 + 09 月 2 笔）⇒ 每月均 <3 ⇒ 不入列（同月口径）
		{Supplier: "供应商戊", SupplierNorm: "供应商戊", BizNo: "E1", BizDate: "2026-08-30", AmountCents: 10000},
		{Supplier: "供应商戊", SupplierNorm: "供应商戊", BizNo: "E2", BizDate: "2026-09-01", AmountCents: 10000},
		{Supplier: "供应商戊", SupplierNorm: "供应商戊", BizNo: "E3", BizDate: "2026-09-02", AmountCents: 10000},
	}
	groups := b.listHighFreqSuppliersUnder1000(rows)
	if len(groups) != 1 {
		t.Fatalf("命中供应商 = %d, 期望 1（仅乙 ≥3 笔且均采一档）: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Supplier != "供应商乙" || g.Month != "2026-09" {
		t.Errorf("组键 = %+v, 期望 供应商乙/2026-09", g)
	}
	if g.Count != 3 || g.SumCents != 60000 {
		t.Errorf("count/sum = %d/%d, 期望 3/60000", g.Count, g.SumCents)
	}
	// ★ 逐笔回溯：清单每一项必须能定位到 L01 原始行（单号 / 供应商原名 / 金额 / 日期）。
	if len(g.Entries) != 3 {
		t.Fatalf("entries = %d, 期望 3（逐笔回溯）", len(g.Entries))
	}
	want := []HighFreqEntry{
		{BizNo: "B1", Supplier: "供应商乙", AmountCents: 10000, Date: "2026-09-01"},
		{BizNo: "B2", Supplier: "供应商乙", AmountCents: 20000, Date: "2026-09-02"},
		{BizNo: "B3", Supplier: "供应商乙", AmountCents: 30000, Date: "2026-09-03"},
	}
	for i, w := range want {
		if g.Entries[i] != w {
			t.Errorf("entries[%d] = %+v, 期望 %+v", i, g.Entries[i], w)
		}
	}
	// count 与明细一致（薄封装：计数 = len(命中组) —— 供 alert 面）。
	if n := b.countHighFreqSuppliersUnder1000(rows); n != len(groups) {
		t.Errorf("count=%d 与明细长度 %d 不一致", n, len(groups))
	}
	// 空行集 ⇒ 0 组（不挂假数据）。
	if got := b.listHighFreqSuppliersUnder1000(nil); len(got) != 0 {
		t.Errorf("空行集应 0 组，实为 %d", len(got))
	}
}

// TestHighFreqSupplierGroupingKeySameAsSplitSuspect ★★ 口径同源断言（机检）：
// 新指标的「同供应商」分组键与 split_suspicion **逐字一致** —— 两者都调 supplierGroupKey
// （= 落库的 supplier_norm，空则退回原名），而**不是各写一份字面量**。
//
// ★ 判别力：若把「新指标」或「split」的分组键改成**原名**（去掉 supplier_norm）⇒ 同样
//
//	两行会被拆成两组 ⇒ 本用例**恰红**。用 4 行（>3）使「≥3 → ≥4」这类**别的**变异不误伤本用例。
func TestHighFreqSupplierGroupingKeySameAsSplitSuspect(t *testing.T) {
	b := &Builder{splitCents: 100000}
	// 同一家供应商的三种**写法**（空格 / 全角 / 括号后缀写在原名里），但 supplier_norm 相同
	// —— 归一后必须并为**同一组**（R-20：去空白 ＋ 全半角归一 ＋ 大小写归一）。
	rows := []Row{
		{Supplier: "供应商甲", SupplierNorm: "供应商甲股份", PurposeL2: "滤布", BizNo: "P1", BizDate: "2026-09-05", AmountCents: 30000},
		{Supplier: "供应商甲 ", SupplierNorm: "供应商甲股份", PurposeL2: "滤布", BizNo: "P2", BizDate: "2026-09-06", AmountCents: 30000},
		{Supplier: "供应商甲　", SupplierNorm: "供应商甲股份", PurposeL2: "滤布", BizNo: "P3", BizDate: "2026-09-07", AmountCents: 30000},
		{Supplier: "供应商甲（新写法）", SupplierNorm: "供应商甲股份", PurposeL2: "滤布", BizNo: "P4", BizDate: "2026-09-08", AmountCents: 30000},
	}
	wantKey := supplierGroupKey(rows[0])
	if wantKey != "供应商甲股份" {
		t.Fatalf("supplierGroupKey = %q, 期望 供应商甲股份（应取 supplier_norm 而非原名）", wantKey)
	}

	// 新指标：4 笔同供应商当月 ⇒ 1 组，组键 = 归一键（若用原名 ⇒ 4 组）
	hf := b.listHighFreqSuppliersUnder1000(rows)
	if len(hf) != 1 {
		t.Fatalf("高频供应商组 = %d, 期望 1（三种写法须并为同一归一主体）: %+v", len(hf), hf)
	}
	if hf[0].Supplier != wantKey {
		t.Errorf("高频供应商组键 = %q, 期望 %q（与 supplierGroupKey 逐字一致）", hf[0].Supplier, wantKey)
	}

	// split_suspicion：同供应商 + 同品类 + 当月，累计 120000 ≥ 阈值 ⇒ 1 组
	sp := b.listSplitSuspect(rows)
	if len(sp) != 1 {
		t.Fatalf("拆分嫌疑组 = %d, 期望 1（三种写法须并为同一归一主体）: %+v", len(sp), sp)
	}
	// ★ 逐字一致：两个同族指标的分组键必须相等（同一把尺子）。
	if sp[0].Supplier != hf[0].Supplier {
		t.Errorf("同族分组键不一致：split=%q, high_freq=%q（口径分裂）", sp[0].Supplier, hf[0].Supplier)
	}
	if sp[0].Supplier != wantKey {
		t.Errorf("split 组键 = %q, 期望 %q（应同用 supplierGroupKey）", sp[0].Supplier, wantKey)
	}
}

// TestSupplierGroupKeyFallback 归一键兜底：supplier_norm 为空（历史行未回填）⇒ 退回原名，
// 绝不因缺归一值而漏计（与既有两处同款行为）。
func TestSupplierGroupKeyFallback(t *testing.T) {
	if got := supplierGroupKey(Row{Supplier: "某供应商"}); got != "某供应商" {
		t.Errorf("无 norm 时应退回原名，got %q", got)
	}
	if got := supplierGroupKey(Row{Supplier: "原名", SupplierNorm: "归一键"}); got != "归一键" {
		t.Errorf("有 norm 时应优先取 norm，got %q", got)
	}
}
