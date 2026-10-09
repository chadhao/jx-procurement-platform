package httpapi

// T1 指标 key 对齐验收（dashboard.json known_gaps 第 7 条 · 两份真相收敛为 spec 唯一真相）。
//
// ★ 双向：
//   正向 —— 聚合路径实际输出的 alerts key 集合 ⊆ spec indicators[*].key，且数量相等；
//   反向（鉴别力）—— 把任一实现 key 换回旧名，同一断言必须红（同 N-026 同族：
//   断言对「旧名」敏感，否则这条测试没有鉴别力）。

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	storetest "github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	"github.com/labstack/echo/v4"
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

// TestDashboardAlertKeysMatchSpec 正向：看板 16 聚合路径输出的 12 个 key 与 spec 完全一致。
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
	// ★ r8 归位：render=alert 的只进 alerts、render=chart 的进 charts —— 15 号板灰态下
	//   alerts 应恰剩 abnormal_subject_hint，by_* 3 个在 charts 且带 not_connected。
	as, _ := data["alerts"].([]any)
	byKey := map[string]map[string]any{}
	for _, a := range as {
		m, _ := a.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	if len(as) != 1 {
		t.Errorf("15 灰态 alerts = %d, want 1（render=alert 只有 abnormal_subject_hint；chart 指标不得混入）", len(as))
	}
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
	// 其余 3 个分布指标（render=chart）：归位到 charts，not_connected 且文案不同
	cs, _ := data["charts"].([]any)
	chartByKey := map[string]map[string]any{}
	for _, c := range cs {
		m, _ := c.(map[string]any)
		chartByKey[m["key"].(string)] = m
	}
	for _, k := range []string{"by_department", "by_category", "by_supplier"} {
		m, ok := chartByKey[k]
		if !ok {
			t.Errorf("charts 缺 render=chart 指标 %s（归位错误）: %v", k, chartByKey)
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
//	空库 ⇒ 12 个指标**全部** not_connected（每个都自证没数据，而非报 0）；
//	只播 L09 ⇒ L09 系 3 个指标出数字，其余仍 not_connected —— 守卫是**指标级**的，
//	不是看板级一刀切（r6：12 个指标分布在 6 个台账上 —— source_ledgers＝L01,L02,L03,L06,L09,L12）。
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
		if len(out) != 14 {
			t.Fatalf("alerts 数 = %d, want 14（N-078 Q3：第 14 指标 low_value_high_freq_supplier）", len(out))
		}
		return out
	}

	// ① 空库：12 项全部自证未接入（**没有一项显示 0**）
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
	// ★ N-046 段③（列级守卫）：L09 有行但 ext 无 is_anomaly_listed 键 ⇒ not_connected 且无 count
	if m := got["change_anomaly_listed"]; m == nil {
		t.Fatal("缺指标 change_anomaly_listed")
	} else if m["status"] != "not_connected" {
		t.Errorf("change_anomaly_listed 列未登记应 not_connected（不得报 0）: %v", m)
	} else if _, has := m["count"]; has {
		t.Errorf("change_anomaly_listed 列未登记不得带 count: %v", m)
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

// ---------- T1c/T3：13/14/15 双向 key 验收 ＋ render 归位 ----------

// renderKeysOf 按 render 从 spec 取该形态的指标 key 集合。
func renderKeysOf(t *testing.T, boardID int, render string) map[string]bool {
	t.Helper()
	b := metaTestBundle(t).Dashboard.Board(boardID)
	if b == nil {
		t.Fatalf("spec 缺看板 %d", boardID)
	}
	out := map[string]bool{}
	for _, ind := range b.Indicators {
		if ind.Render == render {
			out[ind.Key] = true
		}
	}
	return out
}

func keysOfItems(items []any) []string {
	out := []string{}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			if k, ok := m["key"].(string); ok {
				out = append(out, k)
			}
		}
	}
	return out
}

// TestRenderPlacementBoard14 聚合路径 14：每指标按 render 归位 ——
// card→cards、chart→charts、alert→alerts（应为空）、supervision→supervision 块；
// 且各容器 key 集合与 spec 该形态集合**完全一致**（spec ⊆ 输出 ＋ 等量）。
func TestRenderPlacementBoard14(t *testing.T) {
	e, db, auth := newDashboardApp(t) // 不注 Spec ⇒ 聚合路径
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)

	// card / chart / alert：容器 key 与 spec 逐 key 集合相等
	assertAlertKeysAligned(t, keysOfItems(data["cards"].([]any)), renderKeysOf(t, 14, "card"))
	assertAlertKeysAligned(t, keysOfItems(data["charts"].([]any)), renderKeysOf(t, 14, "chart"))
	alertKeys := keysOfItems(data["alerts"].([]any))
	assertAlertKeysAligned(t, alertKeys, renderKeysOf(t, 14, "alert")) // 14 无 alert ⇒ 恰为空

	// supervision：render=supervision 的 1 个指标 → Supervision 块必须存在且有集中度数据结构
	if len(renderKeysOf(t, 14, "supervision")) != 1 {
		t.Fatal("spec 14 应恰有 1 个 render=supervision 指标（前提破坏？spec 演进请更新本用例）")
	}
	sup, ok := data["supervision"].(map[string]any)
	if !ok {
		t.Fatal("14 号板缺 supervision 块（purchaser_concentration 未归位）")
	}
	if _, has := sup["handler_concentration"]; !has {
		t.Errorf("supervision 块缺集中度结构（归位后仍是历史结构键）: %v", sup)
	}
	// 反向：purchaser_concentration 不得以 key 形态出现在 cards/charts/alerts（归位=只此一处）
	for _, container := range []struct {
		name string
		keys []string
	}{{"cards", keysOfItems(data["cards"].([]any))},
		{"charts", keysOfItems(data["charts"].([]any))},
		{"alerts", keysOfItems(data["alerts"].([]any))}} {
		for _, k := range container.keys {
			if k == "purchaser_concentration" {
				t.Errorf("purchaser_concentration 出现在 %s —— render=supervision 应只归位到 supervision 块", container.name)
			}
		}
	}
}

// TestRenderPlacementBoard15 聚合路径 15：恰 4 指标 —— 3 chart ＋ 1 alert，
// **cards 必须为空**（N-033② 裁定删除 expense_total/expense_count），
// 且 6 个旧名（3 个改名前 ＋ 3 个已删）逐个必红。
func TestRenderPlacementBoard15(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")
	seedArchive(t, db, "L05", "EX-2609-1", "ou_a", "生产部", "", "备品备件", "耗材", "2026-09-10", 500000)

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/15?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)

	// cards 必须为空（被裁定删除的 expense_total / expense_count 不得藏进别的 key）
	if cs, _ := data["cards"].([]any); len(cs) != 0 {
		t.Errorf("15 号板 cards 应为空（spec 无 render=card 指标），实际: %v", cs)
	}
	// charts 恰 3 个 spec key；alerts 恰 abnormal_subject_hint
	assertAlertKeysAligned(t, keysOfItems(data["charts"].([]any)), renderKeysOf(t, 15, "chart"))
	assertAlertKeysAligned(t, keysOfItems(data["alerts"].([]any)), renderKeysOf(t, 15, "alert"))

	// 总数恰 4（不多不少 —— 「删掉的 3 个」没有藏在任何容器）
	total := len(keysOfItems(data["cards"].([]any))) +
		len(keysOfItems(data["charts"].([]any))) +
		len(keysOfItems(data["alerts"].([]any)))
	if total != 4 {
		t.Errorf("15 号板指标总数 = %d, want 4（删除裁定的执行验证）", total)
	}

	// 反向（鉴别力）：6 个旧名逐个注入断言必须红
	spec15 := map[string]bool{}
	for k := range renderKeysOf(t, 15, "chart") {
		spec15[k] = true
	}
	for k := range renderKeysOf(t, 15, "alert") {
		spec15[k] = true
	}
	oldNames := []string{
		"expense_by_department", "expense_by_category", "expense_by_supplier", // 改名前
		"expense_total", "expense_count", "expense_monthly_trend", // 已裁定删除
	}
	for _, old := range oldNames {
		if spec15[old] {
			t.Fatalf("前提破坏：旧名 %q 竟在 spec 中", old)
		}
		if !alertKeyMismatch([]string{old}, spec15) {
			t.Errorf("旧名 %q 未被断言抓出 —— 无鉴别力", old)
		}
	}
}

// TestBoard13GrayKeys 13（not_enabled 灰态）：4 个指标按 render 归位且全部灰态
// （spec 指标 key 天然进容器 —— 灰态路径用 spec key，本用例锁「13 也在覆盖内」）。
func TestBoard13GrayKeys(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t) // 注入 Spec ⇒ 灰态
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/13?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if ss, _ := data["source_status"].(string); ss != "not_enabled" {
		t.Errorf("13 source_status=%v, want not_enabled", data["source_status"])
	}
	total := 0
	for _, container := range []string{"cards", "charts", "alerts"} {
		items, _ := data[container].([]any)
		got := keysOfItems(items)
		total += len(got)
		for _, k := range got {
			// 每个输出 key 必须在 spec 13 中（13 灰态全 spec key）
			found := false
			for _, sk := range []string{"budget_progress_by_subject", "overspend_warning_count", "monthly_execution_rate", "yoy"} {
				if sk == k {
					found = true
				}
			}
			if !found {
				t.Errorf("13 输出 key %q 不在 spec 中", k)
			}
		}
	}
	if total != 4 {
		t.Errorf("13 灰态指标总数 = %d, want 4", total)
	}
}

// TestAccountChangedColumnGuard N-034：`L06.收款账户已核验`（SUB 落账带入，存 archive ext）
// 列级守卫 —— L06 有行但该列**无人登记** ⇒ not_connected（而非 0）；登记「否」后出数。
func TestAccountChangedColumnGuard(t *testing.T) {
	e, db, auth := newDashboardApp(t) // 聚合路径
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	fetch := func() map[string]any {
		t.Helper()
		rec, env := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_pm"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
		}
		for _, a := range mustData(t, env)["alerts"].([]any) {
			m, _ := a.(map[string]any)
			if m["key"] == "account_changed" {
				return m
			}
		}
		t.Fatal("缺 account_changed")
		return nil
	}

	// ① L06 有行但该列未登记（ext 无 payee_account_verified）⇒ not_connected
	seedArchiveDocExt(t, db, "L06", "SUB-2609-0001", "SUB", "ou_a", "生产部", "", "2026-09-01", 1000, `{}`)
	m := fetch()
	if m["status"] != "not_connected" {
		t.Errorf("列未登记时 status=%v（%v），期望 not_connected —— 0 与「没登记」同形", m["status"], m)
	}
	if _, has := m["count"]; has {
		t.Errorf("列未登记却出数 count=%v", m["count"])
	}

	// ② 登记为「否」（＝变更过）⇒ 出数 1；再登记一笔「是」⇒ 仍 1（只数「否」）
	seedArchiveDocExt(t, db, "L06", "SUB-2609-0001", "SUB", "ou_a", "生产部", "", "2026-09-01", 1000,
		`{"payee_account_verified": false}`)
	m = fetch()
	if v, has := m["count"]; !has || v != float64(1) {
		t.Errorf("登记「否」后 count=%v（has=%v）, 期望 1（%v）", m["count"], has, m)
	}
	if m["status"] == "not_connected" {
		t.Error("已登记仍报 not_connected —— 守卫过严")
	}
	seedArchiveDocExt(t, db, "L06", "SUB-2609-0002", "SUB", "ou_a", "生产部", "", "2026-09-02", 1000,
		`{"payee_account_verified": true}`)
	m = fetch()
	if v, has := m["count"]; !has || v != float64(1) {
		t.Errorf("「是」不应计入 count=%v（has=%v），期望仍 1（%v）", m["count"], has, m)
	}
}

// TestAccountChangedBindsSpecFields N-034 第 4 条（本批最重要）：把**实现的取数台账**
// 与 spec 的 fields 绑起来 —— 「规格说 L06、实现读 L08」这类差异此前在测试上完全无声
// （[D7] 只校验 spec 内部一致性，管不到实现读了哪张表）。
//
//	正例：只在 **L06** 造行（含该列）⇒ 出数；
//	反例：只在 **L08** 造行（含旧列）⇒ 必须 not_connected ——
//	实现若读回 L08，此断言必红（L08 的行会给守卫供源、从而出数）。
func TestAccountChangedBindsSpecFields(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	fetch := func() map[string]any {
		t.Helper()
		rec, env := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_pm"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
		}
		for _, a := range mustData(t, env)["alerts"].([]any) {
			m, _ := a.(map[string]any)
			if m["key"] == "account_changed" {
				return m
			}
		}
		t.Fatal("缺 account_changed")
		return nil
	}

	// ★ 前提核对：spec 必须仍指向 L06（若 WB 再次改判，本用例随 spec 演进更新 —— N-034 口径）
	specBoard := metaTestBundle(t).Dashboard.Board(16)
	if specBoard == nil {
		t.Fatal("spec 缺 16 号板")
	}
	var specFields []string
	for _, ind := range specBoard.Indicators {
		if ind.Key == "account_changed" {
			specFields = ind.Fields
		}
	}
	if len(specFields) == 0 || specFields[0] != "L06.收款账户已核验" {
		t.Fatalf("spec account_changed fields=%v, 期望 L06.收款账户已核验（改判后本用例须随 spec 更新）", specFields)
	}

	// ① 反例先行：只在 L08 造行（旧数据源、旧列）⇒ 出不了数
	seedArchiveDocExt(t, db, "L08", "V-9999", "CT", "ou_a", "生产部", "供应商", "2026-09-01", 0,
		`{"账户变更": "是"}`)
	m := fetch()
	if _, has := m["count"]; has || m["status"] != "not_connected" {
		t.Errorf("只在 L08 造行却出数/未灰（%v）—— 实现若读 L08 即中招（本断言是 N-034 的绑定覆盖）", m)
	}

	// ② 正例：L06 造行且该列登记为「否」⇒ 出数 1
	seedArchiveDocExt(t, db, "L06", "SUB-2609-0100", "SUB", "ou_a", "生产部", "", "2026-09-01", 1000,
		`{"payee_account_verified": false}`)
	m = fetch()
	if v, has := m["count"]; !has || v != float64(1) {
		t.Errorf("L06 造行后 count=%v（has=%v），期望 1（%v）", m["count"], has, m)
	}
}

// TestDashboardChangeAnomalyListed N-046 段①②（handler 级，真 HTTP，不手搓 payload）：
// ① 正例：ext = {exception_type:采购变更, is_anomaly_listed:true} ⇒ count=1 且出数；
// ② 反例（验「且」双向）：变更但 listed=false ⇒ 不计；紧急但 listed=true ⇒ 也不计。
func TestDashboardChangeAnomalyListed(t *testing.T) {
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
		out := map[string]map[string]any{}
		for _, a := range data["alerts"].([]any) {
			m, _ := a.(map[string]any)
			out[m["key"].(string)] = m
		}
		return out
	}

	// 段① 正例
	seedArchiveExt(t, db, "L09", "EX-2609-C1", "ou_a", "生产部", 1000,
		`{"exception_type":"采购变更","is_anomaly_listed":true}`)
	m := fetch()["change_anomaly_listed"]
	if m == nil {
		t.Fatal("缺指标 change_anomaly_listed")
	}
	if c, _ := m["count"].(float64); c != 1 {
		t.Errorf("段① 正例 count=%v, 期望 1（status=%v）", m["count"], m["status"])
	}
	if m["status"] == "not_connected" {
		t.Errorf("段① 已有登记值却报 not_connected（守卫误伤）: %v", m)
	}

	// 段② 反例（「且」两向都验）
	seedArchiveExt(t, db, "L09", "EX-2609-C2", "ou_b", "生产部", 1000,
		`{"exception_type":"采购变更","is_anomaly_listed":false}`)
	seedArchiveExt(t, db, "L09", "EX-2609-C3", "ou_c", "生产部", 1000,
		`{"exception_type":"紧急采购","is_anomaly_listed":true}`)
	m2 := fetch()["change_anomaly_listed"]
	if c, _ := m2["count"].(float64); c != 1 {
		t.Errorf("段② 反例后 count=%v, 期望仍 1（false 不计、紧急不计；m=%v）", m2["count"], m2)
	}

	// 段②′ 值形态兼容：字符串「是」应计（ext 往返形态），「否」不计
	seedArchiveExt(t, db, "L09", "EX-2609-C4", "ou_d", "生产部", 1000,
		`{"exception_type":"采购变更","is_anomaly_listed":"是"}`)
	seedArchiveExt(t, db, "L09", "EX-2609-C5", "ou_e", "生产部", 1000,
		`{"exception_type":"采购变更","is_anomaly_listed":"否"}`)
	m3 := fetch()["change_anomaly_listed"]
	if c, _ := m3["count"].(float64); c != 2 {
		t.Errorf("字符串形态后 count=%v, 期望 2（「是」计、「否」不计；m=%v）", m3["count"], m3)
	}
}

// ---- N-060 H2(b)：submit_overdue 走 guardedAlert 分支（生产路径）的覆盖 ----
// ★ 背景：newDashboardApp 无 Spec ⇒ deadlineWorkdays=0 ⇒ 走 undefined_criteria 分支 ⇒
// guardedAlert 分支（key 字面量）此前无自动化断言（G2-M3 变异仍绿之因）。
// 本用例：Spec 注入 ＋ 看板16 内存置 connected（绕过 r1 灰态）⇒ 走聚合+guardedAlert。

func newDashboardAppConnected(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator) {
	t.Helper()
	db := storetest.NewDB(t)
	perm := permission.NewLoader(db)
	sessions := access.NewStore(db, "test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)
	bundle := metaTestBundle(t)
	for i := range bundle.Dashboard.Dashboards {
		if bundle.Dashboard.Dashboards[i].ID == 16 {
			bundle.Dashboard.Dashboards[i].SourceStatus = "connected" // 内存 mutate（spec 文件零改动）
		}
	}
	d := Deps{
		Env: &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"},
		DB:  db, Log: observ.NewLogger("error", io.Discard), Metrics: observ.NewMetrics(),
		Perm: perm, Auth: auth, Maps: &config.Maps{}, Spec: bundle,
	}
	e := echo.New()
	api := e.Group("/api", d.requireSession)
	api.GET("/dashboard/:id", d.handleDashboard)
	api.GET("/dashboard/:id/export", d.handleDashboardExport)
	return e, db, auth
}

func TestSubmitOverdueGuardedBranch(t *testing.T) {
	e, db, auth := newDashboardAppConnected(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")
	fetch := func() map[string]map[string]any {
		t.Helper()
		rec, env := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_pm"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("http=%d body=%s", rec.Code, rec.Body.String())
		}
		data := mustData(t, env)
		out := map[string]map[string]any{}
		for _, a := range data["alerts"].([]any) {
			m, _ := a.(map[string]any)
			out[m["key"].(string)] = m
		}
		return out
	}

	// ① 空库（L06 无行）⇒ guardedAlert 分支输出 not_connected 且**无 count**（r3 守卫）
	// ★ 这条断言只在 Spec/connected 路径下有意义 —— undefined 分支输出 undefined_criteria，
	//   两分支由 submitOverdueKey 单源常量钉住（改常量 ⇒ 本断言 + key_align 双红）。
	m0 := fetch()["submit_overdue"]
	if m0 == nil {
		t.Fatal("缺指标 submit_overdue（常量单源后 key 应与 spec 一致）")
	}
	if m0["status"] != "not_connected" {
		t.Errorf("guarded 分支空库应 not_connected（r3），实为 %v（走错分支？）", m0)
	}
	if _, has := m0["count"]; has {
		t.Errorf("not_connected 不得带 count：%v", m0)
	}

	// ② 播 L06 超期行 ⇒ count=1（guardedAlert 真实计数面）
	seedArchiveExt(t, db, "L06", "SUB-2610-0001", "ou_pm", "综合运营部", 100,
		`{"hunan_completed_at":"2026-09-01"}`) // 完成日早于账期 ⇒ 超 3 工作日
	seedOps(t, db, "L06", "SUB-2610-0001", `{}`) // submit_group_at 空 ⇒ 公式前半成立
	m1 := fetch()["submit_overdue"]
	if m1 == nil {
		t.Fatal("缺指标 submit_overdue")
	}
	if c, has := m1["count"]; !has || c != float64(1) {
		t.Errorf("超期未提交 count=%v(has=%v), 期望 1（guardedAlert 分支计数）", m1["count"], has)
	}
	if m1["status"] == "not_connected" {
		t.Errorf("有行不得 not_connected：%v", m1)
	}
}
