package httpapi

// T1 指标 key 对齐验收（dashboard.json known_gaps 第 7 条 · 两份真相收敛为 spec 唯一真相）。
//
// ★ 双向：
//   正向 —— 聚合路径实际输出的 alerts key 集合 ⊆ spec indicators[*].key，且数量相等；
//   反向（鉴别力）—— 把任一实现 key 换回旧名，同一断言必须红（同 N-026 同族：
//   断言对「旧名」敏感，否则这条测试没有鉴别力）。

import (
	"context"
	"net/http"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/seed"
)

// specAlertKeys 看板 spec 指标 key 集合（真 spec 直读，不复制快照 —— N-008）。
func specAlertKeys(t *testing.T, boardID int) map[string]bool {
	t.Helper()
	b := metaTestBundle(t)
	board := b.Dashboard.Board(boardID)
	if board == nil {
		t.Fatalf("spec 缺看板 %d", boardID)
	}
	keys := map[string]bool{}
	for _, ind := range board.Indicators {
		keys[ind.Key] = true
	}
	if len(keys) == 0 {
		t.Fatalf("看板 %d 指标为空", boardID)
	}
	return keys
}

// assertAlertKeysAligned 实现侧 alerts key 集合必须与 spec 集合**完全一致**：
// ⊆（输出不得有 spec 外 key）+ 等量（spec 指标不得缺席 —— 少报一个同样是错位）。
func assertAlertKeysAligned(t *testing.T, got []string, specKeys map[string]bool) {
	t.Helper()
	for _, k := range got {
		if !specKeys[k] {
			t.Errorf("输出指标 key %q 不在 spec indicators 中 —— 两份真相未收敛（改回旧名即触发本断言）", k)
		}
	}
	if len(got) != len(specKeys) {
		t.Errorf("输出指标数 = %d, spec 指标数 = %d —— 数量必须相等（少报/多报都是错位）",
			len(got), len(specKeys))
	}
}

// TestDashboardAlertKeysMatchSpec 正向：看板 16 聚合路径输出的 11 个 key 与 spec 完全一致。
// （newDashboardApp 不注入 Spec ⇒ 走聚合路径 —— 正是两份真相里「代码旧 key」那一路。）
func TestDashboardAlertKeysMatchSpec(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	as, _ := data["alerts"].([]any)
	if len(as) == 0 {
		t.Fatal("聚合路径无 alerts 输出")
	}
	got := make([]string, 0, len(as))
	for _, a := range as {
		m, _ := a.(map[string]any)
		k, _ := m["key"].(string)
		got = append(got, k)
	}
	assertAlertKeysAligned(t, got, specAlertKeys(t, 16))
}

// TestDashboardAlertKeysRejectOldName 反向（鉴别力探针）：注入任一**旧 key**，
// 同一断言必须红 —— 证明断言能抓「改回旧名」，而不是恒真摆设。
func TestDashboardAlertKeysRejectOldName(t *testing.T) {
	specKeys := specAlertKeys(t, 16)
	oldNames := []string{
		"overdue_review", // ← 改回旧名即触发
		"emergency_unclosed_over_24h",
		"single_source",
		"account_change",
		"split_suspect",
		"handler_overdue",
		"requester_as_handler",
		"handler_concentration",
		"group_rejected_undisposed",
	}
	for _, old := range oldNames {
		if specKeys[old] {
			t.Fatalf("前提破坏：旧名 %q 竟在 spec 中（spec 演进了？更新本用例）", old)
		}
		// 构造「实现输出旧名」的形态，断言必须报错
		got := make([]string, 0, len(specKeys))
		first := true
		for k := range specKeys {
			if first {
				got = append(got, old) // 第一个换成旧名
				first = false
				continue
			}
			got = append(got, k)
		}
		if !alertKeyMismatch(got, specKeys) {
			t.Errorf("旧名 %q 未被断言抓出 —— 断言无鉴别力", old)
		}
	}
}

// alertKeyMismatch assertAlertKeysAligned 的可断言化封装（true = 抓到不一致）。
func alertKeyMismatch(got []string, specKeys map[string]bool) bool {
	mismatch := false
	for _, k := range got {
		if !specKeys[k] {
			mismatch = true
		}
	}
	if len(got) != len(specKeys) {
		mismatch = true
	}
	return mismatch
}

// ---------- T3 第三种态：口径未定（global_rules.r7）----------

// TestDashboardUndefinedCriteriaGray 灰态（看板 15 pending）：formula 含「未定义」的指标
// 出 undefined_criteria「口径未定」，其余指标 not_connected「数据未接入」—— 两文案互不相同。
func TestDashboardUndefinedCriteriaGray(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t) // 注入 Spec ⇒ 灰态
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/15?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	as, _ := data["alerts"].([]any)
	byKey := map[string]map[string]any{}
	for _, a := range as {
		m, _ := a.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	// abnormal_subject_hint：第三态（口径未定），不得是「数据未接入」
	abn, ok := byKey["abnormal_subject_hint"]
	if !ok {
		t.Fatalf("缺 spec key abnormal_subject_hint: %v", byKey)
	}
	if abn["status"] != "undefined_criteria" {
		t.Errorf("abnormal_subject_hint status=%v, 期望 undefined_criteria（r7）", abn["status"])
	}
	if msg, _ := abn["message"].(string); msg == "" || msg == "数据未接入" {
		t.Errorf("口径未定文案必须与「数据未接入」不同: %q", abn["message"])
	}
	// 其余 3 个分布指标：not_connected，且文案不同
	for _, k := range []string{"by_department", "by_category", "by_supplier"} {
		m, ok := byKey[k]
		if !ok {
			t.Errorf("缺指标 %s", k)
			continue
		}
		if m["status"] != "not_connected" {
			t.Errorf("%s status=%v, 期望 not_connected", k, m["status"])
		}
		if m["message"] == abn["message"] {
			t.Errorf("%s 文案与口径未定相同 —— 三种态必须互不相同", k)
		}
	}
}

// TestDashboardUndefinedCriteriaAggregated 聚合路径（不注 Spec）：自编口径已删除 ——
// abnormal_subject_hint 恒为 undefined_criteria、不带任何 count（"我方不编"）。
func TestDashboardUndefinedCriteriaAggregated(t *testing.T) {
	e, db, auth := newDashboardApp(t) // 不注入 Spec ⇒ 聚合路径
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")
	// 播 L05 数据：即便源有数，口径未定仍不许出数字
	seedArchive(t, db, "L05", "EX-2609-1", "ou_a", "生产部", "", "备品备件", "耗材", "2026-09-10", 500000)

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/15?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	as, _ := data["alerts"].([]any)
	if len(as) != 1 {
		t.Fatalf("15 号板 alerts = %d, want 1", len(as))
	}
	m, _ := as[0].(map[string]any)
	if m["key"] != "abnormal_subject_hint" {
		t.Errorf("key=%v, 期望 spec key abnormal_subject_hint（自编 key abnormal_subject 须消失）", m["key"])
	}
	if m["status"] != "undefined_criteria" {
		t.Errorf("status=%v, 期望 undefined_criteria", m["status"])
	}
	if v, has := m["count"]; has {
		t.Errorf("口径未定指标携带 count=%v —— 不得显示任何数字", v)
	}
}

// ---------- T2 指标级可用性守卫（connected_requires / global_rules.r6）----------

// TestDashboardGuardPerIndicator 16 号板聚合路径（不注 Spec）：
//
//	空库 ⇒ 11 个指标**全部** not_connected（每个都自证没数据，而非报 0）；
//	只播 L09 ⇒ L09 系 3 个指标出数字，其余仍 not_connected —— 守卫是**指标级**的，
//	不是看板级一刀切（r6：11 个指标分布在 7 个台账上）。
func TestDashboardGuardPerIndicator(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	fetch := func() map[string]map[string]any {
		t.Helper()
		rec, env := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_pm"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
		}
		data := mustData(t, env)
		as, _ := data["alerts"].([]any)
		out := map[string]map[string]any{}
		for _, a := range as {
			m, _ := a.(map[string]any)
			out[m["key"].(string)] = m
		}
		if len(out) != 11 {
			t.Fatalf("alerts 数 = %d, want 11", len(out))
		}
		return out
	}

	// ① 空库：11 项全部自证未接入（**没有一项显示 0**）
	empty := fetch()
	for k, m := range empty {
		st, _ := m["status"].(string)
		if st != "not_connected" && st != "undefined_criteria" {
			t.Errorf("空库下 %s status=%v, 期望 not_connected（不得报 0）", k, m["status"])
		}
		if v, has := m["count"]; has {
			t.Errorf("空库下 %s 带 count=%v —— 0 与「没接上」同形（r3/r6 要防的形态）", k, v)
		}
	}

	// ② 只播 L09 一行：L09 系 3 项应转出数字，L01/L03 系仍 not_connected
	seedArchiveExt(t, db, "L09", "EX-2609-G1", "ou_a", "生产部", 1000, `{"采购方式":"紧急采购"}`)
	seedOps(t, db, "L09", "EX-2609-G1", `{"采购方式":"紧急采购","例外类型":"紧急采购"}`)
	got := fetch()
	for _, k := range []string{"emergency_purchase", "sole_source", "emergency_not_closed_24h"} {
		m := got[k]
		if m == nil {
			t.Fatalf("缺指标 %s", k)
		}
		if _, has := m["count"]; !has {
			t.Errorf("%s 源已有行却仍无 count（守卫误伤）: %v", k, m)
		}
		if m["status"] == "not_connected" {
			t.Errorf("%s 源已有行仍报 not_connected（守卫过严）", k)
		}
	}
	for _, k := range []string{"split_suspicion", "purchaser_overdue", "over_budget"} {
		m := got[k]
		if m == nil {
			t.Fatalf("缺指标 %s", k)
		}
		if m["status"] != "not_connected" {
			t.Errorf("%s 源仍为空却出数: %v（守卫失灵）", k, m)
		}
	}
}

// TestDashboardGuardFalsePositiveNoGuard 14 号板：in_flight_orders **不守卫**
// （connected_requires 明示其 0 是真实语义）—— 空库也必须出 count=0 而非 not_connected。
func TestDashboardNoGuardRealZero(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("http=%d", rec.Code)
	}
	data := mustData(t, env)
	cs, _ := data["cards"].([]any)
	byKey := map[string]map[string]any{}
	for _, c := range cs {
		m, _ := c.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	flight, ok := byKey["in_flight_orders"]
	if !ok {
		t.Fatalf("缺卡片 in_flight_orders: %v", byKey)
	}
	if flight["status"] == "not_connected" {
		t.Errorf("in_flight_orders 被守卫了 —— 其 0 是真实语义（connected_requires 14 明示可不守卫）")
	}
	if v, _ := flight["value"].(float64); v != 0 {
		t.Errorf("空库 in_flight_orders value=%v, 期望真实 0", flight["value"])
	}
	// avg_cycle_days **须守卫**：无完成日期 ⇒ not_connected 而非 0/空
	cycle, ok := byKey["avg_cycle_days"]
	if !ok {
		t.Fatal("缺卡片 avg_cycle_days")
	}
	if cycle["status"] != "not_connected" {
		t.Errorf("avg_cycle_days status=%v, 期望 not_connected（须守卫项）", cycle["status"])
	}
}
