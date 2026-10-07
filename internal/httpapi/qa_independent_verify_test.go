package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	fsync "github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

// 本文件为 QA 独立验证（新增，不修改既有文件），覆盖 B31 / B32(月份口径) / B34 / B35 / B36 / B37，
// 以及脏 JSON、open_id 前缀越权等边界。全部经**真实 HTTP 路径**（而非直接调用内部函数）验证。

// qaNewAppWithMaps 装配完整路由，但**使用真实装载的配置映射**（含阈值），
// 以便验证依赖 d.Maps 的行为（如「同供应商当月累计」阈值）。既有 newAdminTestApp 用空 Maps，不适用。
// 返回 worker 以便驱动真实入库（dev 注入 → 作业处理 → Ingest）。
func qaNewAppWithMaps(t *testing.T, payload *config.ImportPayload, client *feishu.FakeClient) (*echo.Echo, *store.DB, *access.Authenticator, *worker.Worker) {
	t.Helper()
	db := storetest.NewDB(t)
	ctx := context.Background()
	if payload != nil {
		if _, err := config.ImportMappings(ctx, db, payload); err != nil {
			t.Fatalf("导入配置映射失败: %v", err)
		}
	}
	maps, err := config.LoadMaps(ctx, db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	if client == nil {
		client = &feishu.FakeClient{}
	}
	inboxSvc := inbox.NewService(db, metrics, nil)
	ingestor := worker.NewIngestor(db, maps, nil)
	wk := worker.NewWorker(db, client, ingestor, metrics, nil)
	sub := fsync.NewSubscriber(db, client, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore("qa-verify-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)
	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub,
		Perm: perm, Auth: auth, Maps: maps, WebUI: nil, Version: "test",
	})
	return e, db, auth, wk
}

// ---------------- B31：变更链金额同义键随 deny 裁剪 ----------------

// TestQAB31AmountSynonymKeysHiddenFromDeniedRoles 用**两种禁金额角色**（采购经办人 / 验收人）
// 访问变更链，证明：顶层与 archive 内的金额同义键（change_cents / *_display / original_cents）在**任意层级**
// 均不出现，而 archive.contract_no 必须仍在；同时项目总经理（不禁金额）必须仍能看到金额。
// ★ N-075（Q2）：原第三名「系统管理员(ALL+禁金额)」persona 已移除 —— 系统角色不再进
//
//	权限矩阵、不参与业务可见性，仅系统角色者对变更链为**直接 403**（负向由
//	sys_role_n075_test 判据②承载），不再有「可见行但禁金额列」形态。
func TestQAB31AmountSynonymKeysHiddenFromDeniedRoles(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_h", Role: "采购经办人", Active: true},        // ASSIGNED + 禁金额
		store.UserRole{OpenID: "ou_v", Role: "验收人", Active: true},          // PARTICIPATED + 禁金额
		store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true}, // ALL + 不禁金额
	)
	// 变更单：行内 identity 键同时命中 ASSIGNED(ou_h) 与 PARTICIPATED(ou_v)。
	seedArchiveExt(t, db, "L09", "CH-QA-B31-1", "ou_a", "生产部", 100000,
		`{"contract_no":"CT-QA-B31","assigned_open_id":"ou_h","acceptors":["ou_v"],
		  "change_cents":100000,"change_display":"1,000.00",
		  "original_cents":500000,"original_display":"5,000.00"}`)

	// ★ N-042：ASSIGNED/PARTICIPATED 改读规范列（0019）—— seed 补两列
	if _, err := db.ExecContext(ctx,
		`UPDATE t_ledger_archive SET designated_open_id='ou_h', acceptors='["ou_v"]' WHERE ledger_type='L09' AND biz_no='CH-QA-B31-1'`); err != nil {
		t.Fatal(err)
	}
	amountKeys := []string{"change_cents", "change_display", "original_cents", "original_display"}
	denied := []struct {
		name   string
		cookie string
	}{
		{"采购经办人(ASSIGNED)", auth.Establish("ou_h")},
		{"验收人(PARTICIPATED)", auth.Establish("ou_v")},
	}
	for _, d := range denied {
		rec, env := doRequest(e, http.MethodGet, "/api/contract/CT-QA-B31/changes", d.cookie, "")
		if rec.Code != http.StatusOK || env.Code != codeOK {
			t.Fatalf("%s 变更链查询: http=%d code=%d body=%s", d.name, rec.Code, env.Code, rec.Body.String())
		}
		data := mustData(t, env)
		if data["amount_hidden"] != true {
			t.Errorf("%s: amount_hidden=%v, 期望 true", d.name, data["amount_hidden"])
		}
		if _, ok := data["cumulative_change_cents"]; ok {
			t.Errorf("%s: 仍收到 cumulative_change_cents（汇总金额未随 deny 裁剪）", d.name)
		}
		items := data["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("%s: items=%d, 期望 1（行级 scope 未命中，无法验证列裁剪）", d.name, len(items))
		}
		item := items[0].(map[string]any)
		for _, k := range amountKeys {
			if _, ok := item[k]; ok {
				t.Errorf("%s: 顶层泄漏金额同义键 %s", d.name, k)
			}
		}
		arc, ok := item["archive"].(map[string]any)
		if !ok {
			t.Fatalf("%s: archive 块缺失或类型异常: %T", d.name, item["archive"])
		}
		for _, k := range amountKeys {
			if _, ok := arc[k]; ok {
				t.Errorf("%s: archive 嵌套块内泄漏金额同义键 %s", d.name, k)
			}
		}
		if arc["contract_no"] != "CT-QA-B31" {
			t.Errorf("%s: archive.contract_no 被误删: %v", d.name, arc["contract_no"])
		}
		// 记录残余：tier 文本含金额（见报告 Findings）
		t.Logf("[观察] %s: item.tier=%v（由 max 取档派生，仍含金额文本）", d.name, item["tier"])
	}

	// 项目总经理（不禁金额）：金额必须仍在。
	_, envGM := doRequest(e, http.MethodGet, "/api/contract/CT-QA-B31/changes", auth.Establish("ou_gm"), "")
	dGM := mustData(t, envGM)
	itemGM := dGM["items"].([]any)[0].(map[string]any)
	if _, ok := itemGM["change_cents"]; !ok {
		t.Errorf("项目总经理应能看到 change_cents（收紧过度，丢了合法数据）")
	}
	if _, ok := dGM["cumulative_change_cents"]; !ok {
		t.Errorf("项目总经理应能看到累计变更金额（收紧过度）")
	}
}

// ---------------- B36：URL 台账必须等于行的 ledger_type ----------------

// TestQAB36PatchLedgerRejectsMismatchedTable 用**台账 A 的记录 id** 去 PATCH **台账 B**：
// 必须 400/40000，且**不得**写入 ops（否则会出现「写进去了却读不到」的错位数据）。
func TestQAB36PatchLedgerRejectsMismatchedTable(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})

	// 台账 A = L01 的一行。
	seedArchiveExt(t, db, "L01", "BA-QA-B36", "ou_a", "生产部", 100, `{}`)
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM t_ledger_archive WHERE ledger_type='L01' AND biz_no='BA-QA-B36'`).Scan(&id); err != nil {
		t.Fatalf("取 L01 行 id 失败: %v", err)
	}

	// 用 L01 的 id 去 PATCH 台账 B（L04）。
	rec, env := doRequest(e, http.MethodPatch, "/api/ledger/L04/"+itoaTest(id), auth.Establish("ou_ops"),
		`{"fields":{"经办状态":"已完成"}}`)
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Fatalf("跨台账 PATCH 应 400/40000: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_ledger_ops WHERE ledger_type='L04' AND biz_no='BA-QA-B36'`); n != 0 {
		t.Errorf("跨台账写入产生了 %d 行错位 ops（本应一条都不写）", n)
	}
}

// ---------------- B34：台账字段定义白名单（既有键放行 / 大小写·空白归一） ----------------

// TestQAB34WhitelistLegacyKeyAndNormalization 覆盖 B34 ②（行内既有键放行）与 ④（键名大小写/空白归一）。
func TestQAB34WhitelistLegacyKeyAndNormalization(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})

	// 台账 L01 登记**一个**字段定义（键名刻意带前后空白，验证归一）。
	if err := db.UpsertLedgerFieldDef(ctx, store.LedgerFieldDef{LedgerType: "L01", FieldKey: "  付款凭据号  "}); err != nil {
		t.Fatalf("写字段定义失败: %v", err)
	}
	seedArchiveExt(t, db, "L01", "BA-QA-B34", "ou_a", "生产部", 100, `{}`)
	// 行内既有一个「遗留键」（在登记字段定义之前就写入 ops 的键）。
	if err := db.UpsertOps(ctx, &store.LedgerOps{LedgerType: "L01", BizNo: "BA-QA-B34", OpsJSON: `{"抽查状态":"进行中"}`}); err != nil {
		t.Fatalf("写遗留 ops 失败: %v", err)
	}
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM t_ledger_archive WHERE ledger_type='L01' AND biz_no='BA-QA-B34'`).Scan(&id); err != nil {
		t.Fatalf("取 id 失败: %v", err)
	}
	url := "/api/ledger/L01/" + itoaTest(id)
	cookie := auth.Establish("ou_ops")

	// ② 未登记的新键（在 writable 内但行内不存在）→ 40901。
	rec, env := doRequest(e, http.MethodPatch, url, cookie, `{"fields":{"经办状态":"已完成"}}`)
	if rec.Code != http.StatusConflict || env.Code != codeReadOnly {
		t.Errorf("未登记的新键应 409/40901: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}

	// ② 行内既有（遗留）键 → 必须放行（否则前端整行回写会被挡）。
	rec, env = doRequest(e, http.MethodPatch, url, cookie, `{"fields":{"抽查状态":"已完成"}}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Errorf("行内既有键应放行: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}

	// ④ 已登记键（登记时带空白）→ 写入时不带空白，仍应命中（TrimSpace 归一）。
	rec, env = doRequest(e, http.MethodPatch, url, cookie, `{"fields":{"付款凭据号":"PZ-QA-1"}}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Errorf("已登记键（空白归一后）应放行: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
}

// TestQAB34CaseInsensitiveAcrossWriteAndDef 大小写差异不得造成「白名单命中但定义未命中」的不一致。
func TestQAB34CaseInsensitiveAcrossWriteAndDef(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	// 给「采购经办人」一个 ALL 范围、可写字段仅 Status_Code 的规则（覆盖默认）。
	if err := db.UpsertPermissionRule(ctx, store.PermissionRule{
		Resource: "ledger:*", Role: "采购经办人", RowScope: "ALL",
		WritableFields: []string{"Status_Code"},
	}); err != nil {
		t.Fatalf("写规则失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_h", Role: "采购经办人", Active: true})
	// 字段定义登记为大写；写入用小写。
	if err := db.UpsertLedgerFieldDef(ctx, store.LedgerFieldDef{LedgerType: "L01", FieldKey: "STATUS_CODE"}); err != nil {
		t.Fatalf("写字段定义失败: %v", err)
	}
	seedArchiveExt(t, db, "L01", "BA-QA-B34-CASE", "ou_a", "生产部", 100, `{}`)
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM t_ledger_archive WHERE ledger_type='L01' AND biz_no='BA-QA-B34-CASE'`).Scan(&id); err != nil {
		t.Fatalf("取 id 失败: %v", err)
	}
	rec, env := doRequest(e, http.MethodPatch, "/api/ledger/L01/"+itoaTest(id), auth.Establish("ou_h"),
		`{"fields":{"status_code":"OK"}}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Errorf("大小写差异不应造成白名单/定义不一致: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
}

// ---------------- B32：同供应商「当月」累计（月份口径 + 端点） ----------------

// TestQAB32SupplierMonthAccumulation 证明：①月份键为 YYYY-MM（非整年）；②跨月不被累计；③同月被累计；④阈值含端点（>=）。
func TestQAB32SupplierMonthAccumulation(t *testing.T) {
	// 阈值 split_supplier_month = 1000 元 = 100000 分。
	e, db, auth, _ := qaNewAppWithMaps(t, &config.ImportPayload{
		Threshold: []config.ImportKV{{Key: "split_supplier_month", Value: "1000"}},
	}, nil)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})

	// 同一供应商：9 月两笔各 600.00（合计 1200 ≥ 1000）、10 月一笔 600.00（不得并入 9 月）。
	seedArchive(t, db, "L04", "CT-QA-B32-9A", "ou_a", "生产部", "同一供应商", "", "", "2026-09-05", 60000)
	seedArchive(t, db, "L04", "CT-QA-B32-9B", "ou_a", "生产部", "同一供应商", "", "", "2026-09-06", 60000)
	seedArchive(t, db, "L04", "CT-QA-B32-10A", "ou_a", "生产部", "同一供应商", "", "", "2026-10-05", 60000)
	// 另一供应商：9 月恰好 1000.00（端点）。
	seedArchive(t, db, "L04", "CT-QA-B32-EXACT", "ou_b", "生产部", "端点供应商", "", "", "2026-09-07", 100000)

	cookie := auth.Establish("ou_gm")
	flagsOf := func(bizNo string) map[string]any {
		t.Helper()
		_, env := doRequest(e, http.MethodGet, "/api/ledger/L04?biz_no="+bizNo, cookie, "")
		items := mustData(t, env)["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("未取到 %s 行", bizNo)
		}
		row := items[0].(map[string]any)
		ff, ok := row["formula_flags"].(map[string]any)
		if !ok {
			t.Fatalf("%s 缺 formula_flags: %v", bizNo, row)
		}
		sms, ok := ff["supplier_month_sum"].(map[string]any)
		if !ok {
			t.Fatalf("%s 缺 supplier_month_sum: %v", bizNo, ff)
		}
		return sms
	}

	// ① 同月两笔被累计：120000 分（不含 10 月）。
	sms9A := flagsOf("CT-QA-B32-9A")
	if sms9A["month"] != "2026-09" {
		t.Errorf("9A month=%v, 期望 2026-09（不得为整年 '2026'）", sms9A["month"])
	}
	if sms9A["sum_cents"] != float64(120000) {
		t.Errorf("9A sum_cents=%v, 期望 120000（同月 600+600 被累计）", sms9A["sum_cents"])
	}
	if sms9A["over_threshold"] != true {
		t.Errorf("9A over_threshold=%v, 期望 true（1200≥1000）", sms9A["over_threshold"])
	}

	// ② 跨月不被累计：10 月行 sum 只 60000。
	sms10 := flagsOf("CT-QA-B32-10A")
	if sms10["month"] != "2026-10" {
		t.Errorf("10A month=%v, 期望 2026-10", sms10["month"])
	}
	if sms10["sum_cents"] != float64(60000) {
		t.Errorf("10A sum_cents=%v, 期望 60000（不得把 9 月的并进来）", sms10["sum_cents"])
	}
	if sms10["over_threshold"] != false {
		t.Errorf("10A over_threshold=%v, 期望 false", sms10["over_threshold"])
	}

	// ③ 阈值含端点：恰好 100000 → over_threshold true（>=）。
	smsE := flagsOf("CT-QA-B32-EXACT")
	if smsE["sum_cents"] != float64(100000) || smsE["over_threshold"] != true {
		t.Errorf("端点 sum_cents=%v over=%v, 期望 100000 / true（含端点）", smsE["sum_cents"], smsE["over_threshold"])
	}
}

// ---------------- B35：报送部门补全 + 看板 16 的 DEPT 行级收敛 ----------------

// TestQAB35SubmissionDeptFallbackAndDashboard16 证明：POST /api/submission **不传 department** 时，
// 落库 department 退用登记人部门（非空）；看板 16 的 group_rejected_undisposed 在 DEPT 角色下按部门收敛、不恒为 0。
func TestQAB35SubmissionDeptFallbackAndDashboard16(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t) // 完整路由：同时有 /api/submission 与 /api/dashboard
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Department: "运营部", Active: true},
		store.UserRole{OpenID: "ou_lead", Role: roleDeptLead, Department: "运营部", Active: true},
	)
	opsCookie := auth.Establish("ou_ops")

	// ① 不传 department 登记。
	rec, env := doRequest(e, http.MethodPost, "/api/submission", opsCookie,
		`{"biz_no":"SUB-QA-B35-1","subject_type":"公户付款"}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("登记报送: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	subID := int64(mustData(t, env)["id"].(float64))

	// ② 落库 department 必须非空，且等于登记人部门。
	var dept string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(department,'') FROM t_submission WHERE id=?`, subID).Scan(&dept); err != nil {
		t.Fatalf("回读 department 失败: %v", err)
	}
	if dept == "" {
		t.Fatalf("提交未带 department 时落库为空 —— DEPT/CHARGE_DEPT 令牌将永远命中 0 条（B35 未生效）")
	}
	if dept != "运营部" {
		t.Errorf("department=%q, 期望 运营部（退用登记人部门）", dept)
	}

	// ③ 标记为「集团已驳回、未处置」（仅改 grp_state，不填 reject_reason）。
	rec, env = doRequest(e, http.MethodPost, "/api/submission/"+itoaTest(subID)+"/group", opsCookie,
		`{"grp_state":"已驳回"}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("登记集团驳回: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}

	// ④ 看板 16 在 dashboard.json source_status≠connected 期走 **r1 灰态**：
	//    指标显示「数据未接入」而非数字（DEPT 行级收敛的数字断言迁至
	//    TestDashboardGroupRejectedScopedByRowScope —— 那里的 app 不注入 Spec，
	//    聚合路径可达；本 app 镜像生产，恒注入 Spec）。
	_, env16 := doRequest(e, http.MethodGet, "/api/dashboard/16?period=2026-09", auth.Establish("ou_lead"), "")
	data16 := mustData(t, env16)
	if data16["source_status"] == "connected" {
		t.Errorf("spec source_status=pending，响应却为 connected")
	}
	as, _ := data16["alerts"].([]any)
	var hit map[string]any
	for _, a := range as {
		m, _ := a.(map[string]any)
		if m["key"] == "group_rejected_unhandled" {
			hit = m
		}
	}
	if hit == nil {
		t.Fatalf("灰态告警清单缺 spec 指标 key group_rejected_unhandled: %v", as)
	}
	if hit["status"] != "not_connected" {
		t.Errorf("group_rejected_unhandled status=%v, 期望 not_connected（r1）", hit["status"])
	}
	if _, has := hit["count"]; has {
		t.Errorf("灰态指标携带 count —— r1 禁止显示数字: %v", hit)
	}
}

// ---------------- B37：看板 14 走 L11 派生口径 ----------------

// TestQAB37Dashboard14DerivedNonEmptyAndIgnoresFakeL11 证明看板 14 的五项指标由 L04+L07 派生算出非空，
// 且**不受**伪造 L11 存档/运营行影响；未到货合同计为在途且无 delay_days。
func TestQAB37Dashboard14DerivedNonEmptyAndIgnoresFakeL11(t *testing.T) {
	e, db, auth := newDashboardApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", roleProjectGM, "")

	// 合同 1：交期 09-06，关联 GR 实际到货 09-09 → 延期 3 天；业务日期 09-01。
	seedArchiveDocExt(t, db, "L04", "CT-QA-B37-1", "CT", "ou_a", "生产部", "供应商甲", "2026-09-01", 130000,
		`{"delivery_date":"2026-09-06"}`)
	seedArchiveDocExt(t, db, "L07", "GR-QA-B37-1", "GR", "ou_a", "生产部", "供应商甲", "2026-09-09", 130000,
		`{"related_biz_no":"CT-QA-B37-1"}`)
	// 合同 2：未到货（无 GR）→ 应计在途。
	seedArchiveDocExt(t, db, "L04", "CT-QA-B37-2", "CT", "ou_a", "生产部", "供应商乙", "2026-09-02", 70000,
		`{"delivery_date":"2026-09-20"}`)
	// 负向素材：伪造 L11 存档 + 伪造运营行（若旧实现回归，下面指标会被污染）。
	seedArchiveDocExt(t, db, "L11", "CT-FAKE-9999", "CT", "ou_zz", "生产部", "脏供应商", "2026-09-01", 99999999, `{}`)
	seedOps(t, db, "L11", "CT-FAKE-9999", `{"实际到货":"2026-09-02","延期天数":999,"完成日期":"2026-09-02"}`)

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("采购执行看板: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)

	// ① 在途订单数 = 1（仅合同 2；伪造 L11 有到货，不应被算进来）。
	if v, _ := cardValue(t, data, "in_flight_orders"); v != float64(1) {
		t.Errorf("in_flight_orders=%v, 期望 1", v)
	}
	// ② 平均采购周期 = 8 天（合同1 09-01→到货 09-09）；空值即派生失效。
	if v, ok := cardValue(t, data, "avg_cycle_days"); !ok || v != float64(8) {
		t.Errorf("avg_cycle_days=%v, 期望 8（L11 派生未生效？）", v)
	}

	charts := data["charts"].([]any)
	seriesOf := func(key string) []any {
		for _, c := range charts {
			m := c.(map[string]any)
			if m["key"] == key {
				s, _ := m["series"].([]any)
				return s
			}
		}
		return nil
	}

	// ③ 延期订单 TOP5：只应含 CT-QA-B37-1（3 天），不得含伪造 L11 的 999。
	delay := seriesOf("delay_top5")
	if len(delay) != 1 {
		t.Fatalf("delay_top5 条数=%d, 期望 1（%v）", len(delay), delay)
	}
	d0 := delay[0].(map[string]any)
	if d0["x"] != "CT-QA-B37-1" || d0["y"] != float64(3) {
		t.Errorf("delay_top5[0]=%v, 期望 CT-QA-B37-1/3", d0)
	}
	for _, it := range delay {
		if it.(map[string]any)["x"] == "CT-FAKE-9999" {
			t.Errorf("delay_top5 被伪造 L11 行污染（读到 t_ledger_archive 的 L11 行）")
		}
	}

	// ④ 月度采购金额趋势：2026-09 应为 130000+70000=200000（不含伪造 99999999）。
	var sep map[string]any
	for _, it := range seriesOf("monthly_amount_trend") {
		m := it.(map[string]any)
		if m["x"] == "2026-09" {
			sep = m
		}
	}
	if sep == nil {
		t.Fatalf("月度趋势缺 2026-09 点")
	}
	if sep["amount_cents"] != float64(200000) {
		t.Errorf("2026-09 月度金额=%v, 期望 200000（伪造 L11 未混入）", sep["amount_cents"])
	}

	// ⑤ 同供应商当月累计 TOP：含供应商甲/乙，不含「脏供应商」。
	sup := seriesOf("supplier_monthly_accum_top")
	seen := map[string]bool{}
	for _, it := range sup {
		seen[it.(map[string]any)["x"].(string)] = true
	}
	if !seen["供应商甲"] || !seen["供应商乙"] {
		t.Errorf("同供应商 TOP 缺真实供应商: %v", seen)
	}
	if seen["脏供应商"] {
		t.Errorf("同供应商 TOP 混入伪造 L11 的供应商")
	}

	// ⑥ 未到货的合同不得出现 delay_days（看板级：合同2 不在 delay_top5）。
	for _, it := range delay {
		if it.(map[string]any)["x"] == "CT-QA-B37-2" {
			t.Errorf("未到货合同被计入了延期 TOP5（臆造 delay_days）")
		}
	}
}

// ---------------- 脏数据 / 前缀越权 边界 ----------------

// TestQADirtyJSONNeverBreaksQueries 任意一行 ext_json='not json' 不得使台账列表 / 变更链 / 看板整体失败。
func TestQADirtyJSONNeverBreaksQueries(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})

	// 多张台账各塞一行脏 JSON（含 L04，用于派生路径）。
	for _, x := range []struct{ lt, biz string }{
		{"L04", "CT-DIRTY-1"}, {"L05", "SA-DIRTY-1"}, {"L07", "GR-DIRTY-1"}, {"L09", "CH-DIRTY-1"}, {"L08", "SUP-DIRTY-1"},
	} {
		seedArchiveExt(t, db, x.lt, x.biz, "ou_a", "生产部", 100, `not json`)
	}
	cookie := auth.Establish("ou_gm")

	for _, lt := range []string{"L04", "L05", "L07", "L08", "L09"} {
		rec, _ := doRequest(e, http.MethodGet, "/api/ledger/"+lt, cookie, "")
		if rec.Code != http.StatusOK {
			t.Errorf("台账 %s 列表被脏 JSON 打挂: http=%d", lt, rec.Code)
		}
	}
	// 派生台账 L11（读 L04 脏行 + 关联查询）。
	if rec, _ := doRequest(e, http.MethodGet, "/api/ledger/L11", cookie, ""); rec.Code != http.StatusOK {
		t.Errorf("L11 派生列表被脏 JSON 打挂: http=%d", rec.Code)
	}
	// 变更链（json_extract 守卫）。
	if rec, _ := doRequest(e, http.MethodGet, "/api/contract/CT-DIRTY-1/changes", cookie, ""); rec.Code != http.StatusOK {
		t.Errorf("变更链被脏 JSON 打挂: http=%d", rec.Code)
	}
	// 看板 14/15/16。
	for _, id := range []string{"14", "15", "16"} {
		if rec, _ := doRequest(e, http.MethodGet, "/api/dashboard/"+id+"?period=2026-09", cookie, ""); rec.Code != http.StatusOK {
			t.Errorf("看板 %s 被脏 JSON 打挂: http=%d", id, rec.Code)
		}
	}
}

// TestQAPermissionOpenIDPrefixNotVisible 前缀近似 open_id（ou_ab 是 ou_abc 的前缀）不得互相可见。
func TestQAPermissionOpenIDPrefixNotVisible(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ab", Role: "采购经办人", Active: true},
		store.UserRole{OpenID: "ou_abc", Role: "采购经办人", Active: true},
	)
	seedArchiveExt(t, db, "L09", "CH-PREFIX-1", "ou_x", "生产部", 100,
		`{"contract_no":"CT-PREFIX","assigned_open_id":"ou_abc","change_cents":100}`)
	// ★ N-042：ASSIGNED 改读规范列（0019）—— ou_ab 前缀不得命中 ou_abc（精确等值）
	if _, err := db.ExecContext(ctx,
		`UPDATE t_ledger_archive SET designated_open_id='ou_abc' WHERE ledger_type='L09' AND biz_no='CH-PREFIX-1'`); err != nil {
		t.Fatal(err)
	}

	_, envAB := doRequest(e, http.MethodGet, "/api/contract/CT-PREFIX/changes", auth.Establish("ou_ab"), "")
	if got := mustData(t, envAB)["count"].(float64); got != 0 {
		t.Errorf("前缀近似 open_id(ou_ab) 命中 %v 条，期望 0（前缀越权）", got)
	}
	_, envABC := doRequest(e, http.MethodGet, "/api/contract/CT-PREFIX/changes", auth.Establish("ou_abc"), "")
	if got := mustData(t, envABC)["count"].(float64); got != 1 {
		t.Errorf("本人经办(ou_abc)命中 %v 条，期望 1", got)
	}
}

// ---------------- B37 端到端：映射 → 注入 → Ingest → 派生看板 ----------------

// TestQAB37EndToEndInjectIngestDerived 全程走**真实生产链路**：配置映射 → dev 注入事件 →
// worker 拉详情并 Ingest → 看板 14 由 L04+L07 派生。全程**不手写 ext_json**，
// 证明「field_id→biz_field 映射被真正消费」与「看板 14 派生口径」端到端打通。
func TestQAB37EndToEndInjectIngestDerived(t *testing.T) {
	payload := &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "ac-ct", DocType: "CT"}, {Code: "ac-gr", DocType: "GR"}},
		FieldID: []config.ImportFieldID{
			{DocType: "CT", FieldID: "w_delivery", FieldName: "交期", BizField: "delivery_date"},
			{DocType: "CT", FieldID: "w_amt", FieldName: "合同金额", BizField: "amount_cents"},
			{DocType: "CT", FieldID: "w_sup", FieldName: "供应商", BizField: "supplier"},
			{DocType: "GR", FieldID: "w_related", FieldName: "关联合同号", BizField: "related_biz_no"},
		},
		LedgerType: []config.ImportKV{{Key: "CT", Value: "L04"}, {Key: "GR", Value: "L07"}},
		Threshold:  []config.ImportKV{{Key: "split_supplier_month", Value: "1000"}},
	}
	details := map[string]*feishu.InstanceDetail{
		"I-CT-E2E-1": {
			InstanceCode: "I-CT-E2E-1", ApprovalCode: "ac-ct", StatusRaw: "APPROVED", BizNo: "CT-E2E-1",
			ApplicantOpenID: "ou_a", Department: "生产部", OccurredAt: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
			Fields: []feishu.FieldValue{
				{FieldID: "w_delivery", ValueText: "2026-09-06"},
				{FieldID: "w_amt", ValueText: "1300.00"},
				{FieldID: "w_sup", ValueText: "供应商甲"},
			},
		},
		"I-GR-E2E-1": {
			InstanceCode: "I-GR-E2E-1", ApprovalCode: "ac-gr", StatusRaw: "APPROVED", BizNo: "GR-E2E-1",
			Department: "生产部", OccurredAt: time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC),
			Fields: []feishu.FieldValue{{FieldID: "w_related", ValueText: "CT-E2E-1"}},
		},
		"I-CT-E2E-2": {
			InstanceCode: "I-CT-E2E-2", ApprovalCode: "ac-ct", StatusRaw: "APPROVED", BizNo: "CT-E2E-2",
			ApplicantOpenID: "ou_a", Department: "生产部", OccurredAt: time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC),
			Fields: []feishu.FieldValue{
				{FieldID: "w_delivery", ValueText: "2026-09-20"},
				{FieldID: "w_amt", ValueText: "700.00"},
				{FieldID: "w_sup", ValueText: "供应商乙"},
			},
		},
	}
	client := &feishu.FakeClient{DetailFn: func(_ context.Context, code string) (*feishu.InstanceDetail, error) {
		if d, ok := details[code]; ok {
			return d, nil
		}
		return nil, errors.New("未知实例: " + code)
	}}
	e, db, auth, wk := qaNewAppWithMaps(t, payload, client)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})

	// 真实写入路径：dev 注入事件 → 处理作业（Ingest）。
	for i, inst := range []string{"I-CT-E2E-1", "I-GR-E2E-1", "I-CT-E2E-2"} {
		rec := inject(e, event20("ev-e2e-"+itoaTest(int64(i)), inst, "APPROVED"))
		if rec.Code != http.StatusOK {
			t.Fatalf("注入 %s 失败: http=%d body=%s", inst, rec.Code, rec.Body.String())
		}
		if _, err := wk.ProcessDueOnce(ctx); err != nil {
			t.Fatalf("处理作业失败: %v", err)
		}
	}

	// 自证：Ingest 确实把映射字段写进了 ext_json（否则派生拿不到交期/关联号）。
	var ext, bizDate string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(ext_json,''), COALESCE(biz_date,'') FROM t_ledger_archive WHERE ledger_type='L04' AND biz_no='CT-E2E-1'`).
		Scan(&ext, &bizDate); err != nil {
		t.Fatalf("回读 L04 失败: %v", err)
	}
	if !strings.Contains(ext, "delivery_date") || !strings.Contains(ext, "2026-09-06") {
		t.Fatalf("CT 行 ext_json 未含 delivery_date（字段映射未被消费）: %s", ext)
	}
	if bizDate != "2026-09-01" {
		t.Errorf("CT biz_date=%q, 期望 2026-09-01（Ingest 写入）", bizDate)
	}

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_gm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("看板 14 端到端: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if v, _ := cardValue(t, data, "in_flight_orders"); v != float64(1) {
		t.Errorf("in_flight_orders=%v, 期望 1（CT-E2E-2 未到货）", v)
	}
	if v, _ := cardValue(t, data, "avg_cycle_days"); v != float64(8) {
		t.Errorf("avg_cycle_days=%v, 期望 8（端到端派生 09-01→09-09）", v)
	}
	found := false
	for _, c := range data["charts"].([]any) {
		m := c.(map[string]any)
		if m["key"] != "delay_top5" {
			continue
		}
		for _, it := range m["series"].([]any) {
			p := it.(map[string]any)
			if p["x"] == "CT-E2E-1" && p["y"] == float64(3) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("端到端：delay_top5 未含 CT-E2E-1/3")
	}
}
