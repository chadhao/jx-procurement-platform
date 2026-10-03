package httpapi

// T4 验收（MIMO-NEXT-BATCH §4）：
//   4. `no_self_purchaser`：指定人＝提出人 ⇒ 拦；指定人≠提出人 ⇒ 放行；
//   amount_vs_pr 阈值读 params（改参数 ⇒ 行为变，不写死 10）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// newPRSubmitApp PR 提交夹具（映射/定义/角色/常量齐备，链＝采二档）。
func newPRSubmitApp(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator) {
	t.Helper()
	db := storetest.NewDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatal(err)
	}
	// approval_code ↔ doc_type
	if err := db.UpsertConfigMapping(ctx, store.ConfigMappingRow{
		MapKind: "approval_code", MapKey: "code-pr", MapValue: "PR", DocType: "PR",
	}); err != nil {
		t.Fatal(err)
	}
	apprMap, err := config.LoadApprovalMap(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	maps := &config.Maps{Approval: apprMap, Ledger: map[string][]string{"PR": {"L02", "L03"}}}
	if err := db.UpsertApprovalDef(ctx, &store.ApprovalDef{
		ApprovalCode: "code-pr", DocType: "PR", Name: "物资采购申请单", GroupName: "test",
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// 常量（PR.unit 是 constant_ref）
	if _, err := db.SeedConstants(ctx, "unit", []string{"吨", "件"}); err != nil {
		t.Fatal(err)
	}
	// 链角色：采二档＝主管领导(仓储部) + 合同两级(主管领导/项目总经理) + 综合运营主管×2
	for _, r := range []store.UserRole{
		{OpenID: "ou_app", Name: "申请人", Role: "申请人", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_sup", Name: "主管", Role: "主管领导", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_gm", Name: "总一", Role: "项目总经理", Department: "综合运营部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_ops", Name: "运营", Role: "综合运营主管", Department: "综合运营部", Active: true, UpdatedAt: time.Now()},
	} {
		if err := db.UpsertUserRole(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatal(err)
	}
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)
	flowSvc := flow.NewWithConfig(db, "app", maps, nil)
	chainSvc := &chain.Service{B: bundle, Roles: chainRoleAdapterForTest{db: db}}
	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Perm: perm, Auth: auth, Maps: maps, Version: "test",
		Flow: flowSvc, Spec: bundle, Chain: chainSvc,
	})
	return e, db, auth
}

// ★ N-039：明细行在 fields.detail **数组**内（repeating section）；顶层 amount_cents
// 必须等于服务端汇总（100000×1=100000 分）——%s 注入在 fields 顶层（designated_by 等）。
const prSubmitBodyFmt = `{"doc_type":"PR","approval_code":"code-pr","amount_cents":100000,` +
	`"usage_category_l1":"P01","fields":{` +
	`"usage_category_l1":"P01","usage_category_l2":"主原料","requirement_type":"常规",` +
	`"purpose":"补一批滤布","required_date":"2026-12-01","urgent_level":"常规",` +
	`"budget_subject":"生产预算","is_safety_or_special_equipment":false,"is_fixed_asset":false,` +
	`"detail":[{"material_name_spec":"滤布 1200mm","unit":"吨","quantity":1,` +
	`"estimated_unit_price_cents":100000}]%s}}`

func TestPRSubmitNoSelfPurchaser(t *testing.T) {
	e, _, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")

	// ① 指定人＝提出人 ⇒ 拦（R-27 / 验收 #4）
	code, env := postSubmit(t, e, cookie,
		strings.Replace(prSubmitBodyFmt, "%s", `,"designated_by":"ou_app"`, 1), "")
	if code != http.StatusBadRequest || !strings.Contains(env.Message, "指定人不能是需求提出人本人") {
		t.Fatalf("自派应拦 400，实为 %d（%s）", code, env.Message)
	}

	// ② 指定人≠提出人 ⇒ 放行（上级指派）
	code, env = postSubmit(t, e, cookie,
		strings.Replace(prSubmitBodyFmt, "%s", `,"designated_by":"ou_sup"`, 1), "")
	if code != http.StatusOK {
		t.Fatalf("上级指派应放行，实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	if !strings.HasPrefix(d["biz_no"].(string), "PR-") {
		t.Errorf("biz_no = %v", d["biz_no"])
	}
}

// TestAmountVsPrUsesParamThreshold amount_vs_pr：阈值**读 params**（改参数 ⇒ 行为变）。
func TestAmountVsPrUsesParamThreshold(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// 造关联 PR 实例（预估 100000 分）
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_instance (instance_code, approval_code, doc_type, biz_no, status,
  applicant_open_id, amount_cents, source, created_at, updated_at)
VALUES ('I-PR-2609-0007','code-pr','PR','PR-2609-0007','PENDING','ou_app',100000,'flow',?,?)`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("造 PR 实例失败: %v", err)
	}
	bundle := metaTestBundle(t)
	d := Deps{Spec: bundle, DB: db}
	form := bundle.Forms["CT"]

	mkBody := func(contractCents int64) *approvalSubmitBody {
		return &approvalSubmitBody{
			DocType: "CT", AmountCents: &contractCents,
			Fields: map[string]any{"related_biz_no": "PR-2609-0007"},
		}
	}

	// ① 合同 105000 ≤ 100000×1.10 ⇒ 放行（容差 10% 来自 params）
	if err := checkCTAmountVsPR(ctx, d, form, mkBody(105000), "ou_app"); err != nil {
		t.Errorf("10%% 容差内应放行: %v", err)
	}
	// ② 合同 120000 ⇒ 超容差，提示先走 PC（require_purchase_change）
	err := checkCTAmountVsPR(ctx, d, form, mkBody(120000), "ou_app")
	if err == nil || !strings.Contains(err.Error(), "采购变更") {
		t.Fatalf("超容差应拒并提示 PC: %v", err)
	}
	// ③ ★ 探针：改 params 容差 10 → 30 ⇒ 120000 变为放行（证明阈值没写死）
	mutated := *bundle
	mutated.Params = &specload.ParamsDoc{Params: map[string]specload.ParamEntry{}}
	for k, v := range bundle.Params.Params {
		mutated.Params.Params[k] = v
	}
	e := mutated.Params.Params["contract.amount_over_pr_tolerance_percent"]
	e.Value = json.RawMessage("30")
	mutated.Params.Params["contract.amount_over_pr_tolerance_percent"] = e
	d2 := Deps{Spec: &mutated, DB: db}
	if err := checkCTAmountVsPR(ctx, d2, form, mkBody(120000), "ou_app"); err != nil {
		t.Errorf("容差改 30 后 120000 应放行（参数驱动）: %v", err)
	}
	// ④ 关联 PR 缺失 ⇒ 可见失败（不 fail-open）
	bad := mkBody(100000)
	bad.Fields["related_biz_no"] = "PR-NOT-EXIST"
	if err := checkCTAmountVsPR(ctx, d, form, bad, "ou_app"); err == nil {
		t.Error("关联 PR 不存在必须可见失败（fail-open＝假校验）")
	}
}

// TestCTRouteTwoLevel CT 走合同统一两级（T4 chain 侧）。
func TestCTRouteTwoLevel(t *testing.T) {
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatal(err)
	}
	amt := int64(500000)
	r, err := chain.ResolveRoute(b, chain.Facts{DocType: chain.DocCT, AmountCents: &amt, Department: "生产部"})
	if err != nil {
		t.Fatalf("CT 路线解析失败: %v", err)
	}
	if r.RouteID != "contract_two_level" {
		t.Errorf("CT route = %s，应为 contract_two_level", r.RouteID)
	}
	nodes, err := chain.BuildNodes(b, r.RouteID, chain.Facts{DocType: chain.DocCT, AmountCents: &amt, Department: "生产部", HasContract: true})
	if err != nil {
		t.Fatal(err)
	}
	approvals := 0
	for _, n := range nodes {
		if n.IsApproval {
			approvals++
		}
	}
	if approvals != 2 {
		t.Errorf("CT 审批任务 = %d，应为两级（主管领导 + 项目总经理）", approvals)
	}
	// 不重复插入（hasContractNodes 命中）
	contractCount := 0
	for _, n := range nodes {
		if n.SourceNodeID == "contract_supervisor" {
			contractCount++
		}
	}
	if contractCount != 1 {
		t.Errorf("contract_supervisor 出现 %d 次，应为 1", contractCount)
	}
}
