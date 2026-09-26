package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// newDashboardApp 装配「最小 Echo」：仅注册看板路由 + 会话中间件，不依赖 router.go。
func newDashboardApp(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator) {
	t.Helper()
	db := storetest.NewDB(t)
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)

	d := Deps{
		Env:     &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"},
		DB:      db,
		Log:     observ.NewLogger("error", io.Discard),
		Metrics: observ.NewMetrics(),
		Perm:    perm,
		Auth:    auth,
		Maps:    &config.Maps{},
	}

	e := echo.New()
	api := e.Group("/api", d.requireSession)
	api.GET("/dashboard/:id", d.handleDashboard)
	api.GET("/dashboard/:id/export", d.handleDashboardExport)
	return e, db, auth
}

func seedRole(t *testing.T, db *store.DB, openID, role, dept string) {
	t.Helper()
	if err := db.UpsertUserRole(context.Background(), store.UserRole{
		OpenID: openID, Role: role, Department: dept, Active: true,
	}); err != nil {
		t.Fatalf("写入用户角色失败: %v", err)
	}
}

func seedArchive(t *testing.T, db *store.DB, lt, bizNo, applicant, dept, supplier, l1, l2, bizDate string, amount int64) {
	t.Helper()
	now := time.Now().UTC()
	amt := amount
	if err := db.UpsertArchive(context.Background(), db, &store.LedgerArchive{
		LedgerType: lt, BizNo: bizNo, InstanceCode: "I-" + bizNo, SourceDocType: "PR",
		Department: dept, ApplicantOpenID: applicant, AmountCents: &amt,
		Supplier: supplier, PurposeClassL1: l1, PurposeClassL2: l2, BizDate: bizDate,
		ExtJSON: "{}", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入台账存档失败: %v", err)
	}
}

// seedArchiveDocExt 写入台账行，可指定 source_doc_type 与 ext_json（供 L11 派生路径的用例）。
func seedArchiveDocExt(t *testing.T, db *store.DB, lt, bizNo, docType, applicant, dept, supplier, bizDate string, amount int64, ext string) {
	t.Helper()
	now := time.Now().UTC()
	amt := amount
	if err := db.UpsertArchive(context.Background(), db, &store.LedgerArchive{
		LedgerType: lt, BizNo: bizNo, InstanceCode: "I-" + bizNo, SourceDocType: docType,
		Department: dept, ApplicantOpenID: applicant, AmountCents: &amt,
		Supplier: supplier, BizDate: bizDate, ExtJSON: ext, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入台账存档失败: %v", err)
	}
}

func seedOps(t *testing.T, db *store.DB, lt, bizNo, opsJSON string) {
	t.Helper()
	if err := db.UpsertOps(context.Background(), &store.LedgerOps{
		LedgerType: lt, BizNo: bizNo, OpsJSON: opsJSON, UpdatedBy: "ou_admin",
	}); err != nil {
		t.Fatalf("写入运营表失败: %v", err)
	}
}

func cardsOf(t *testing.T, data map[string]any) []any {
	t.Helper()
	cs, ok := data["cards"].([]any)
	if !ok {
		t.Fatalf("响应缺 cards 字段: %+v", data)
	}
	return cs
}

func cardValue(t *testing.T, data map[string]any, key string) (any, bool) {
	t.Helper()
	for _, c := range cardsOf(t, data) {
		m := c.(map[string]any)
		if m["key"] == key {
			return m["value"], true
		}
	}
	return nil, false
}

func supervisionMap(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	s, ok := data["supervision"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺 supervision 字段: %+v", data)
	}
	return s
}

// TestDashboardPermissionAndForbidden 有权限角色可取数；无权限角色 → 40300（TC-14/A1）。
func TestDashboardPermissionAndForbidden(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")
	seedRole(t, db, "ou_fin", "集团财务", "")

	// 有权限（项目总经理 ALL）→ 200。
	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("项目总经理取数: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	if data := mustData(t, env); data["id"].(float64) != 14 {
		t.Fatalf("看板 id = %v, 期望 14", data["id"])
	}

	// 无权限（集团财务 DENY）→ 403 / 40300。
	rec2, env2 := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_fin"), "")
	if rec2.Code != http.StatusForbidden || env2.Code != codeForbidden {
		t.Fatalf("集团财务越权: http=%d code=%d, 期望 403/40300", rec2.Code, env2.Code)
	}
	// 越权留痕。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_audit_log WHERE action='view' AND result='deny'`); n < 1 {
		t.Errorf("越权访问未留痕（deny 记录数=%d）", n)
	}
}

// TestDashboardBudgetEmpty 预算看板（13）本期不启用 → 空序列、HTTP 200、不报错。
func TestDashboardBudgetEmpty(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/13?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("预算看板空态: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if len(cardsOf(t, data)) != 0 {
		t.Errorf("预算看板应返回空 cards，实际 %d 项", len(cardsOf(t, data)))
	}
	if charts, _ := data["charts"].([]any); len(charts) != 0 {
		t.Errorf("预算看板应返回空 charts，实际 %d 项", len(charts))
	}
}

// TestDashboardSupervisionZeroViolation 监督指标：「需求提出人任经办人的笔数」应恒为 0（TC-24）。
func TestDashboardSupervisionZeroViolation(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	// 两笔：被指定经办人（ou_h1）≠ 需求提出人 → 合规。
	seedArchive(t, db, "L03", "PR-2609-0001", "ou_a1", "生产部", "", "", "备品备件", "2026-09-05", 100000)
	seedOps(t, db, "L03", "PR-2609-0001", `{"assigned_open_id":"ou_h1","经办状态":"进行中"}`)
	seedArchive(t, db, "L03", "PR-2609-0002", "ou_a2", "生产部", "", "", "备品备件", "2026-09-06", 120000)
	seedOps(t, db, "L03", "PR-2609-0002", `{"assigned_open_id":"ou_h1","经办状态":"进行中"}`)

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("异常预警看板: http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	sup := supervisionMap(t, data)
	if got := sup["requester_as_handler_count"].(float64); got != 0 {
		t.Fatalf("需求提出人任经办人的笔数 = %v, 期望 0", got)
	}
	conc, _ := sup["handler_concentration"].([]any)
	if len(conc) == 0 {
		t.Fatal("经办人指定集中度为空（运营表未被读取？）")
	}

	// 反例：需求提出人自任经办人 → 笔数应为 1（证明指标可检出，非恒 0）。
	seedArchive(t, db, "L03", "PR-2609-0003", "ou_same", "生产部", "", "", "备品备件", "2026-09-07", 80000)
	seedOps(t, db, "L03", "PR-2609-0003", `{"assigned_open_id":"ou_same","经办状态":"进行中"}`)

	_, env2 := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_pm"), "")
	sup2 := supervisionMap(t, mustData(t, env2))
	if got := sup2["requester_as_handler_count"].(float64); got != 1 {
		t.Fatalf("反例后需求提出人任经办人的笔数 = %v, 期望 1", got)
	}
}

// TestDashboardSourceFromOps 看板数据源取自运营表：指标值仅存在于 t_ledger_ops 时仍可算出（FR-M5-05）。
func TestDashboardSourceFromOps(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	// ★ L11 订单执行台账是**派生视图**（Q14 定案）：看板不再读 t_ledger_archive 的 L11 行，
	//   而由「L04 合同台账（骨架）+ L07 到货验收（GR）」派生（架构审查 A-1 修复）。
	//   故这里必须造**真实路径**的数据，而不是伪造一行 L11 存档 + 一行 L11 运营。
	seedArchiveDocExt(t, db, "L04", "CT-2609-0001", "CT", "ou_a1", "生产部", "某某五金", "2026-09-01", 130000,
		`{"delivery_date":"2026-09-06"}`)
	seedArchiveDocExt(t, db, "L07", "GR-2609-0001", "GR", "ou_a1", "生产部", "某某五金", "2026-09-08", 130000,
		`{"related_biz_no":"CT-2609-0001"}`)
	// ★ 负向断言素材：故意塞一行「看起来像 L11」的存档 + 运营，证明看板**不会**再读它。
	//   （若旧实现回归，下面 avg_cycle_days 会被这条脏数据影响而失败。）
	seedArchiveDocExt(t, db, "L11", "CT-9999-9999", "CT", "ou_zz", "生产部", "脏数据供应商", "2026-09-01", 99999999,
		`{"delivery_date":"2026-09-01"}`)
	seedOps(t, db, "L11", "CT-9999-9999", `{"实际到货":"2026-09-02","完成日期":"2026-09-02","延期天数":999}`)

	// L03 采购经办登记：被指定经办人仅存在于运营表。
	seedArchive(t, db, "L03", "PR-2609-0010", "ou_a9", "生产部", "", "", "备品备件", "2026-09-05", 100000)
	seedOps(t, db, "L03", "PR-2609-0010", `{"assigned_open_id":"ou_h1","经办状态":"进行中"}`)

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("采购执行看板: http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)

	// 平均采购周期：合同业务日期 2026-09-01 → 到货 2026-09-08 = 7 天（派生路径）。
	// ★ 该值同时是 A-1 的回归断言：脏 L11 存档行若被读入，这里会变成别的数字。
	avg, ok := cardValue(t, data, "avg_cycle_days")
	if !ok {
		t.Fatal("缺 avg_cycle_days 指标卡")
	}
	if v, isNum := avg.(float64); !isNum || v != 7 {
		t.Fatalf("平均采购周期 = %v, 期望 7（运营表未被读取？）", avg)
	}

	// 经办人指定集中度：来自运营表 assigned_open_id → 非空。
	sup := supervisionMap(t, data)
	conc, _ := sup["handler_concentration"].([]any)
	if len(conc) == 0 {
		t.Fatal("经办人指定集中度为空（运营表未被读取？）")
	}
	first := conc[0].(map[string]any)
	if first["handler"] != "ou_h1" {
		t.Fatalf("集中度首位 handler = %v, 期望 ou_h1", first["handler"])
	}
}

// TestDashboardExportAudit 导出复用投影并写审计留痕（FR-M7-03）。
func TestDashboardExportAudit(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")
	seedArchive(t, db, "L03", "PR-2609-0001", "ou_a1", "生产部", "", "", "备品备件", "2026-09-05", 100000)
	seedOps(t, db, "L03", "PR-2609-0001", `{"assigned_open_id":"ou_h1","经办状态":"进行中"}`)

	rec, _ := doRequest(e, http.MethodGet, "/api/dashboard/16/export?period=2026-09&format=csv", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("CSV 导出: http=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get(echo.HeaderContentType); !strings.Contains(ct, "text/csv") {
		t.Errorf("CSV Content-Type = %q", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "requester_as_handler_count") {
		t.Errorf("CSV 未含监督指标列，body=%s", body)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_audit_log WHERE action='export'`); n < 1 {
		t.Errorf("导出未写入审计留痕（export 记录数=%d）", n)
	}

	// XLSX 亦可导出（纯标准库生成）。
	rec2, _ := doRequest(e, http.MethodGet, "/api/dashboard/16/export?period=2026-09&format=xlsx", auth.Establish("ou_pm"), "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("XLSX 导出: http=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if ct := rec2.Header().Get(echo.HeaderContentType); !strings.Contains(ct, "spreadsheetml") {
		t.Errorf("XLSX Content-Type = %q", ct)
	}
}

// TestDashboardColumnProjection 列级投影：金额列对无权限角色不出现在响应中（TC-07 精神）。
func TestDashboardColumnProjection(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	// 验收人：column_deny = 金额列（amount_cents/amount_display）。
	seedRole(t, db, "ou_acceptor", "验收人", "")
	seedArchive(t, db, "L05", "SA-2609-0001", "ou_a1", "生产部", "某某五金", "管理费用", "办公费", "2026-09-05", 50000)

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/15?period=2026-09", auth.Establish("ou_acceptor"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("费用结构看板: http=%d body=%s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	for _, c := range cardsOf(t, data) {
		m := c.(map[string]any)
		if _, leaked := m["amount_cents"]; leaked {
			t.Fatalf("金额列越权泄漏（验收人不应见 amount_cents）: %+v", m)
		}
		if _, leaked := m["amount_display"]; leaked {
			t.Fatalf("金额列越权泄漏（验收人不应见 amount_display）: %+v", m)
		}
	}
}

// ---------- 集成补充：行·列投影在导出路径同样生效 ----------

// deepHasKey 递归判断任意嵌套结构中是否存在指定键（用于列投影的「连字段名都不出现」断言）。
func deepHasKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			if k == key || deepHasKey(vv, key) {
				return true
			}
		}
	case []any:
		for _, vv := range t {
			if deepHasKey(vv, key) {
				return true
			}
		}
	}
	return false
}

// alertCount 从看板响应中取某告警项的 count。
func alertCount(t *testing.T, data map[string]any, key string) float64 {
	t.Helper()
	as, okArr := data["alerts"].([]any)
	if !okArr {
		t.Fatalf("响应缺 alerts 字段: %+v", data)
	}
	for _, a := range as {
		m, okM := a.(map[string]any)
		if !okM || m["key"] != key {
			continue
		}
		v, okV := m["count"].(float64)
		if !okV {
			t.Fatalf("告警 %s 的 count 不是数值: %v", key, m["count"])
		}
		return v
	}
	t.Fatalf("告警项 %s 不存在", key)
	return 0
}

// TestDashboardAmountStrippedFromJSONAndExport ★ 列级投影必须在**导出路径**同样生效。
//
// 该用例是本轮补齐的**关键缺口**：导出接口（/export）最容易「绕过」列过滤，
// 因为它是独立的序列化分支。此处用同一份数据做 A/B：
//   - 项目总经理（ALL、无静态 deny）→ JSON 中**应出现** amount_cents（证明数据本身有金额）；
//   - 系统管理员（ALL、deny=amount_cents/amount_display/formula_flags）→ JSON 与 **CSV 导出**中
//     都**不得出现** amount_cents / amount_display。
func TestDashboardAmountStrippedFromJSONAndExport(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")
	seedRole(t, db, "ou_sys", "系统管理员", "")

	// L05 = 费用类台账（看板 15 的数据源），带金额。
	seedArchive(t, db, "L05", "EX-2609-0001", "ou_a", "生产部", "供应商甲", "耗材", "办公用品", "2026-09-10", 480000)
	seedArchive(t, db, "L05", "EX-2609-0002", "ou_b", "销售部", "供应商乙", "差旅", "市内交通", "2026-09-12", 120000)

	const url = "/api/dashboard/15?period=2026-09"

	// A：项目总经理（ALL，无金额 deny）→ 应能看到金额。
	_, envPM := doRequest(e, http.MethodGet, url, auth.Establish("ou_pm"), "")
	dataPM := mustData(t, envPM)
	if !deepHasKey(dataPM, "amount_cents") {
		t.Fatalf("对照组失效：项目总经理的响应里没有 amount_cents，无法证明投影真的裁剪了（数据可能为空）")
	}

	// B：系统管理员（ALL，deny 金额）→ JSON 中不得有金额键。
	_, envSys := doRequest(e, http.MethodGet, url, auth.Establish("ou_sys"), "")
	dataSys := mustData(t, envSys)
	for _, k := range []string{"amount_cents", "amount_display", "formula_flags"} {
		if deepHasKey(dataSys, k) {
			t.Errorf("JSON 响应泄漏受限列 %s（角色：系统管理员，deny 金额）", k)
		}
	}

	// C：CSV 导出同样不得出现金额列（防「改走导出绕过列过滤」）。
	recExp, _ := doRequest(e, http.MethodGet,
		"/api/dashboard/15/export?period=2026-09&format=csv", auth.Establish("ou_sys"), "")
	if recExp.Code != http.StatusOK {
		t.Fatalf("导出: http=%d body=%s", recExp.Code, recExp.Body.String())
	}
	body := recExp.Body.String()
	for _, k := range []string{"amount_cents", "amount_display"} {
		if strings.Contains(body, k) {
			t.Errorf("CSV 导出泄漏受限列名 %s（导出路径绕过了列投影）", k)
		}
	}
	// 导出行非空（否则上面的断言无意义）。
	if strings.TrimSpace(body) == "" {
		t.Error("CSV 导出内容为空，列投影断言无效")
	}
}

// TestDashboardGroupRejectedScopedByRowScope ★ 「集团驳回后未处置」必须按行范围收敛。
//
// 修复前该指标是全表 COUNT(*)，而默认种子把 dashboard:4 发给了全部 10 个角色
// （含申请人 SELF / 采购经办人 ASSIGNED / 验收人 PARTICIPATED）→ 受限角色读到全局值。
func TestDashboardGroupRejectedScopedByRowScope(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")
	seedRole(t, db, "ou_app", "申请人", "")
	seedRole(t, db, "ou_fin", "集团财务", "")

	// 两条「集团已驳回但未处置」的报送，申请人分别是他俩（Q14-B 第 5 项后 SELF 按
	// applicant_open_id 收敛，故这里必须写真实身份列，而不是只看 created_by）。
	for _, r := range []struct{ bizNo, by string }{
		{"SUB-SC-1", "ou_app"},
		{"SUB-SC-2", "ou_pm"},
	} {
		if _, err := db.CreateSubmission(ctx, &store.Submission{
			BizNo: r.bizNo, SubjectType: "公户付款", SubmitState: "未提交",
			GrpState: "已驳回", RejectReason: "", CreatedBy: r.by,
			ApplicantOpenID: r.by, Department: "生产部",
		}); err != nil {
			t.Fatalf("写入报送失败: %v", err)
		}
	}

	const url = "/api/dashboard/16?period=2026-09"

	// ALL：项目总经理看到全量 2 条。
	_, envPM := doRequest(e, http.MethodGet, url, auth.Establish("ou_pm"), "")
	if got := alertCount(t, mustData(t, envPM), "group_rejected_undisposed"); got != 2 {
		t.Errorf("项目总经理(ALL) 集团驳回未处置 = %v, 期望 2", got)
	}

	// SELF：申请人只统计本人登记的 1 条（不得拿全量 2）。
	_, envApp := doRequest(e, http.MethodGet, url, auth.Establish("ou_app"), "")
	if got := alertCount(t, mustData(t, envApp), "group_rejected_undisposed"); got != 1 {
		t.Errorf("申请人(SELF) 集团驳回未处置 = %v, 期望 1（行级越权：不应看到他人报送的聚合值）", got)
	}

	// DENY：集团财务无该看板权限。
	recFin, envFin := doRequest(e, http.MethodGet, url, auth.Establish("ou_fin"), "")
	if recFin.Code != http.StatusForbidden || envFin.Code != codeForbidden {
		t.Errorf("集团财务越权: http=%d code=%d, 期望 403/40300", recFin.Code, envFin.Code)
	}
}

// TestDashboardInvalidPeriodRejected 非法账期 → 40000（避免静默落到空数据集被误读为「本期无数据」）。
func TestDashboardInvalidPeriodRejected(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	for _, bad := range []string{"2026-13", "2026/09", "abc", "2026-9-1x"} {
		rec, env := doRequest(e, http.MethodGet,
			"/api/dashboard/15?period="+bad, auth.Establish("ou_pm"), "")
		if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
			t.Errorf("账期 %q: http=%d code=%d, 期望 400/40000", bad, rec.Code, env.Code)
		}
	}
	// 合法账期仍应通过。
	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/15?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Errorf("合法账期被拒: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
}
