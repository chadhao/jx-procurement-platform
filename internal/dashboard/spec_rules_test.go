package dashboard

// global_rules r1/r3 落地验收：
//   r1 source_status 非 connected ⇒ 全部指标「数据未接入」而非 0；
//   r3 「应恒为 0」类指标必须带数据源非空前置断言（L03 空 ⇒ not_connected 不报 0）。

import (
	"context"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func testDashDoc(status string) *specload.DashboardDoc {
	return &specload.DashboardDoc{
		Version: "1.0",
		GlobalRules: specload.DashboardRules{
			R1NotConnectedShowsGap:   "r1",
			R2SourceMustBeOpsTable:   "r2",
			R3ZeroNeedsNonemptyGuard: "r3",
		},
		Dashboards: []specload.DashboardBoard{
			{
				ID: 16, Key: "exception_alert", Label: "异常预警面板",
				SourceLedgers: []string{"L03"}, SourceStatus: status,
				Indicators: []specload.IndicatorDoc{
					{Key: "self_purchaser_count", Label: "需求提出人任经办人", Formula: "count"},
					{Key: "overdue_unapproved", Label: "超时未审", Formula: "count"},
				},
			},
		},
	}
}

// r1：pending 看板 —— 指标全部 not_connected、无任何 count=0 冒充真 0。
func TestR1PendingShowsNotConnected(t *testing.T) {
	b := New(nil) // 灰态短路在任何 SQL 之前 —— 不该碰 DB
	b.WithDashboard(testDashDoc("pending"))
	res, err := b.Build(context.Background(), 16, "2026-09", Query{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.SourceStatus != "pending" {
		t.Fatalf("source_status = %q, want pending", res.SourceStatus)
	}
	if len(res.Alerts) != 2 {
		t.Fatalf("alerts = %d, want 2（指标清单来自 spec）", len(res.Alerts))
	}
	for _, a := range res.Alerts {
		if a["status"] != "not_connected" {
			t.Errorf("指标 %v status=%v, 期望 not_connected", a["key"], a["status"])
		}
		if _, has := a["count"]; has {
			t.Errorf("指标 %v 带了 count —— r1 禁止非 connected 显示数字（哪怕是 0）", a["key"])
		}
	}
	// 卡片/图表空序列（不得以 0 值卡面充数）
	if len(res.Cards) != 0 || len(res.Charts) != 0 {
		t.Errorf("灰态下 cards/charts 应为空，实际 cards=%d charts=%d", len(res.Cards), len(res.Charts))
	}
	if res.Supervision["status"] != "not_connected" {
		t.Errorf("supervision 灰态缺失: %+v", res.Supervision)
	}
	if res.SpecVersion != "1.0" {
		t.Errorf("spec_version = %q, want 1.0", res.SpecVersion)
	}
}

// r1：not_enabled 同样灰态（看板 13 的 L12 未启用 —— 如实呈现而非 0%）。
func TestR1NotEnabledShowsNotConnected(t *testing.T) {
	b := New(nil)
	b.WithDashboard(testDashDoc("not_enabled"))
	res, err := b.Build(context.Background(), 16, "2026-09", Query{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.SourceStatus != "not_enabled" {
		t.Fatalf("source_status = %q", res.SourceStatus)
	}
	if len(res.Alerts) == 0 || res.Alerts[0]["status"] != "not_connected" {
		t.Fatal("not_enabled 看板未灰态")
	}
}

// r3：L03 过滤后 0 行 ⇒ source_status=not_connected（「应恒为 0」不得直接报 0）。
func TestR3ZeroGuardEmptySource(t *testing.T) {
	sup := buildSupervision(nil, "2026-09", Query{}, nil)
	if sup["source_status"] != "not_connected" {
		t.Fatalf("L03 空时 source_status = %v, want not_connected（R-02：恒 0 与没接上同形）",
			sup["source_status"])
	}
}

// r3 反面：源有行 ⇒ connected，计数照常（断言不误伤真 0）。
func TestR3ZeroGuardWithSource(t *testing.T) {
	rows := []Row{
		{LedgerType: "L03", BizNo: "PR-1", ApplicantOpenID: "ou_a", BizDate: "2026-09-05", Ops: map[string]any{
			"assigned_open_id": "ou_h1",
		}},
	}
	sup := buildSupervision(rows, "2026-09", Query{}, nil)
	if sup["source_status"] != "connected" {
		t.Fatalf("L03 有行时 source_status = %v, want connected", sup["source_status"])
	}
	if c, _ := sup["requester_as_handler_count"].(int); c != 0 {
		t.Fatalf("合规样本计数 = %d, want 0", c)
	}
}

// TestAssignedFallsBackToDesignatedPurchaser N-015 批 2：审批时点指定经办落
// ext 的规格列名 designated_purchaser —— 看板 Assigned() 必须能读到它
// （否则「需求提出人任经办人」等指标在审批指定路径下永远算不出人）。
func TestAssignedFallsBackToDesignatedPurchaser(t *testing.T) {
	r := Row{ArchiveExt: map[string]any{"designated_purchaser": "ou_h9"}}
	if got := r.Assigned(); got != "ou_h9" {
		t.Fatalf("Assigned() = %q, 期望 designated_purchaser 的值 ou_h9", got)
	}
	// 运营表口径优先（兼容既有人工登记路径不回归）
	r2 := Row{
		Ops:        map[string]any{"assigned_open_id": "ou_ops"},
		ArchiveExt: map[string]any{"designated_purchaser": "ou_h9"},
	}
	if got := r2.Assigned(); got != "ou_ops" {
		t.Fatalf("ops 口径应优先, got %q", got)
	}
}
