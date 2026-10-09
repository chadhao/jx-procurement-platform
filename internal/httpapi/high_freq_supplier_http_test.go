package httpapi

// N-078 Q3：看板 16 第 14 指标 low_value_high_freq_supplier 的**端到端**证据
// （真 HTTP + 聚合路径 + L01 采一档数据）。
//
// ★ 与 internal/dashboard 的单元用例互补：这里证明 key 出现在真实看板响应里、
//   且清单 detail 逐笔回溯到 L01（供应商 / 金额 / 日期 / 单号）。

import (
	"context"
	"net/http"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/seed"
)

func TestDashboardLowValueHighFreqSupplierHTTP(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	// 甲：当月 3 笔、均 <1,000 元 ⇒ 入列。
	seedArchive(t, db, "L01", "BA-2609-0001", "ou_a", "生产部", "供应商甲", "分类", "滤布", "2026-09-01", 20000)
	seedArchive(t, db, "L01", "BA-2609-0002", "ou_a", "生产部", "供应商甲", "分类", "滤布", "2026-09-02", 30000)
	seedArchive(t, db, "L01", "BA-2609-0003", "ou_a", "生产部", "供应商甲", "分类", "滤布", "2026-09-03", 40000)
	// 乙：当月仅 2 笔 ⇒ 不入列。
	seedArchive(t, db, "L01", "BA-2609-0004", "ou_b", "生产部", "供应商乙", "分类", "树脂", "2026-09-01", 10000)
	seedArchive(t, db, "L01", "BA-2609-0005", "ou_b", "生产部", "供应商乙", "分类", "树脂", "2026-09-02", 10000)

	fetch := func() map[string]any {
		t.Helper()
		rec, env := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_pm"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
		}
		for _, a := range mustData(t, env)["alerts"].([]any) {
			m, _ := a.(map[string]any)
			if m["key"] == "low_value_high_freq_supplier" {
				return m
			}
		}
		t.Fatal("缺指标 low_value_high_freq_supplier（spec key 未落到聚合输出）")
		return nil
	}

	m := fetch()
	if c, _ := m["count"].(float64); c != 1 {
		t.Errorf("高频供应商 count=%v, 期望 1（仅甲 ≥3 笔；m=%v）", m["count"], m)
	}
	if m["status"] == "not_connected" {
		t.Errorf("已有 L01 行却报 not_connected（守卫误伤）: %v", m)
	}
	detail, _ := m["detail"].([]any)
	if len(detail) != 1 {
		t.Fatalf("detail 组数 = %d, 期望 1（清单逐笔回溯）: %v", len(detail), m["detail"])
	}
	g, _ := detail[0].(map[string]any)
	if g["supplier"] != "供应商甲" || g["month"] != "2026-09" {
		t.Errorf("清单组键 = %v, 期望 供应商甲/2026-09", g)
	}
	if c, _ := g["count"].(float64); c != 3 {
		t.Errorf("清单组 count = %v, 期望 3", g["count"])
	}
	entries, _ := g["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, 期望 3（供应商/金额/日期逐笔可回溯）", len(entries))
	}
	e0, _ := entries[0].(map[string]any)
	if e0["biz_no"] != "BA-2609-0001" || e0["date"] != "2026-09-01" {
		t.Errorf("entries[0] = %v, 期望含 biz_no=BA-2609-0001 / date=2026-09-01", e0)
	}

	// ★ 采一档范围：把乙补到 3 笔，但其中一笔 ≥1,000 元 ⇒ 仍不入列（采一档仅 2 笔）。
	seedArchive(t, db, "L01", "BA-2609-0006", "ou_b", "生产部", "供应商乙", "分类", "树脂", "2026-09-03", 150000)
	m2 := fetch()
	if c, _ := m2["count"].(float64); c != 1 {
		t.Errorf("补 1 笔 ≥1,000 元后 count=%v, 期望仍 1（≥1,000 元不属采一档，m=%v）", m2["count"], m2)
	}
}
