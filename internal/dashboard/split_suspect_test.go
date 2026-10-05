package dashboard

// N-060 F9（FR-M4-06）：拆分嫌疑从「仅计数」到「组明细可导出」。

import "testing"

func TestListSplitSuspectGroups(t *testing.T) {
	b := &Builder{splitCents: 100000} // 阈值 1,000 元（100000 分）
	rows := []Row{
		// 组 A：同供应商+同品类+同月，2 笔累计 150000 ≥ 阈值 ⇒ 命中（含归一分组键）
		{BizNo: "PR-1", Supplier: "供应商甲", SupplierNorm: "供应商甲股份", PurposeL2: "滤布", BizDate: "2026-09-05", AmountCents: 80000},
		{BizNo: "PR-2", Supplier: "供应商甲（新写法）", SupplierNorm: "供应商甲股份", PurposeL2: "滤布", BizDate: "2026-09-20", AmountCents: 70000},
		// 组 B：2 笔但累计不足阈值 ⇒ 不列
		{BizNo: "PR-3", Supplier: "供应商乙", PurposeL2: "树脂", BizDate: "2026-09-06", AmountCents: 30000},
		{BizNo: "PR-4", Supplier: "供应商乙", PurposeL2: "树脂", BizDate: "2026-09-21", AmountCents: 20000},
		// 单笔 ≥ 阈值但只有 1 笔 ⇒ 不列（≥2 笔条件）
		{BizNo: "PR-5", Supplier: "供应商丙", PurposeL2: "备件", BizDate: "2026-09-07", AmountCents: 200000},
	}
	groups := b.listSplitSuspect(rows)
	if len(groups) != 1 {
		t.Fatalf("命中组 = %d, 期望 1（明细清单）：%+v", len(groups), groups)
	}
	g := groups[0]
	if g.Supplier != "供应商甲股份" || g.PurposeL2 != "滤布" || g.Month != "2026-09" {
		t.Errorf("组键 = %+v, 期望归一分组键 供应商甲股份/滤布/2026-09", g)
	}
	if g.SumCents != 150000 || g.Count != 2 {
		t.Errorf("sum/count = %d/%d, 期望 150000/2", g.SumCents, g.Count)
	}
	if len(g.BizNos) != 2 || g.BizNos[0] != "PR-1" || g.BizNos[1] != "PR-2" {
		t.Errorf("biz_nos = %v, 期望 [PR-1 PR-2]（按行序、供清单导出定位）", g.BizNos)
	}
	// count 与明细一致（薄封装：计数= len(命中组) —— 既有 alert 面行为不变）
	if n := b.countSplitSuspect(rows); n != len(groups) {
		t.Errorf("count=%d 与明细长度 %d 不一致", n, len(groups))
	}
	// 无命中 ⇒ 空明细（组B 不足阈值、PR-5 单笔 —— 均不列；不挂假数据）
	if got := b.listSplitSuspect(rows[2:]); len(got) != 0 {
		t.Errorf("组B+单笔应命中 0 组，实为 %d：%+v", len(got), got)
	}
	if got := b.listSplitSuspect(nil); len(got) != 0 {
		t.Errorf("空行集应 0 组，实为 %d", len(got))
	}
}
