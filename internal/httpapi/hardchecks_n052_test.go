package httpapi

// N-052 批 13 · A13：PR#safety_branch 求值器 ＋ SA 提交端到端（真 spec）＋ 注册表覆盖钉子。
// ★ S1–S3 走真 spec 的 hard 判据（证明 SA 提交路径真的执行 spec）；S4 走
// validateSubmitForm 的 required_conditional（与 hard 判据分开断言）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

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
	storetest "github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	"github.com/labstack/echo/v4"
)

// ---- §1.1 safety_branch 条件型四例（直调求值器，同 bj 先例）----

func TestN052SafetyBranchFourCases(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["PR"]
	ctx := context.Background()

	block := func(name string, fields map[string]any) {
		t.Helper()
		mustBlock(t, name, checkPRSafetyBranch(ctx, d, form, n47Body("PR", nil, fields), "ou_app"))
	}
	pass := func(name string, fields map[string]any) {
		t.Helper()
		mustPass(t, name, checkPRSafetyBranch(ctx, d, form, n47Body("PR", nil, fields), "ou_app"))
	}
	// 命中 ∧ 缺 ⇒ 拒
	block("true ∧ 空 ⇒ 拒", map[string]any{"is_safety_or_special_equipment": true})
	// 命中 ∧ 有值 ⇒ 放
	pass("true ∧ 有值 ⇒ 放", map[string]any{
		"is_safety_or_special_equipment": true, "qualification_doc": "资质 X"})
	// ★ 未命中 ∧ 空 ⇒ 放（不反向）
	pass("false ∧ 空 ⇒ 放（不反向）", map[string]any{
		"is_safety_or_special_equipment": false})
	// 未命中（缺键）∧ 空 ⇒ 放
	pass("缺键 ∧ 空 ⇒ 放", map[string]any{})
	// 未命中 ∧ 有值 ⇒ 放
	pass("false ∧ 有值 ⇒ 放", map[string]any{
		"is_safety_or_special_equipment": false, "qualification_doc": "随手传的"})
	// 字符串形态 "true" 命中（表单往返）
	block("字符串 true ∧ 空 ⇒ 拒", map[string]any{"is_safety_or_special_equipment": "true"})
}

// ---- 注册项钉子（M3 鉴别力）：合成 severity=hard 的 safety_branch 走
// evaluateHardChecks —— 注册在 ⇒ 求值器执行；摘注册 ⇒ fail-closed「未实现求值器」。
// ★ 真 spec 的 safety_branch 仍为 soft（severity 翻转归我方）⇒ S4 走结构化路径、
// 不经过本函数；本用例预演 WB 翻 hard 后的执行面（也给 M3 提供鉴别力）。----

func TestN052SafetyBranchRegisteredInEvaluate(t *testing.T) {
	form := n047Form(t, "PR", "safety_branch") // 合成：只留 safety_branch 且置 hard
	// 注册在 ∧ true ∧ 有值 ⇒ 放
	mustPass(t, "hard∧注册在∧有值 ⇒ 放", runN47(t, form, n47Body("PR", nil, map[string]any{
		"is_safety_or_special_equipment": true, "qualification_doc": "资质 X"})))
	// 注册在 ∧ true ∧ 空 ⇒ hard 拒（点名 qualification_doc）
	mustBlock(t, "hard∧注册在∧true∧空 ⇒ 拒", runN47(t, form, n47Body("PR", nil, map[string]any{
		"is_safety_or_special_equipment": true})))
	// 注册在 ∧ false ∧ 空 ⇒ 放（不反向）
	mustPass(t, "hard∧注册在∧false∧空 ⇒ 放", runN47(t, form, n47Body("PR", nil, map[string]any{
		"is_safety_or_special_equipment": false})))
}

// ---- §1.2 SA 提交端到端（handler 级 · 真 spec）----

func newSASubmitApp(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator) {
	return newSASubmitAppWith(t, nil)
}

// newSASubmitAppWith 可变体：mutate 在装载后、建 router 前执行 —— N-054 ① 用它把
// cross_month_allocation 的 severity 在**内存**里置 hard（spec 文件零改动），预演我方
// 验收后翻 hard 的终态（soft 不经 evaluateHardChecks ⇒ 拦截例在 handler 级结构上不可达）。
func newSASubmitAppWith(t *testing.T, mutate func(*specload.Bundle)) (*echo.Echo, *store.DB, *access.Authenticator) {
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
	if err := db.UpsertConfigMapping(ctx, store.ConfigMappingRow{
		MapKind: "approval_code", MapKey: "code-sa", MapValue: "SA", DocType: "SA",
	}); err != nil {
		t.Fatal(err)
	}
	apprMap, err := config.LoadApprovalMap(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	maps := &config.Maps{Approval: apprMap, Ledger: map[string][]string{"SA": {"L05"}}}
	if err := db.UpsertApprovalDef(ctx, &store.ApprovalDef{
		ApprovalCode: "code-sa", DocType: "SA", Name: "费用事前申请单", GroupName: "test",
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// expense_sales 链角色：applicant / supervisor / ops_supervisor（chain.json#routes.expense_sales）
	for _, r := range []store.UserRole{
		{OpenID: "ou_app", Name: "申请人", Role: "申请人", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_sup", Name: "主管", Role: "主管领导", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
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
	if mutate != nil {
		mutate(bundle)
	}
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	perm := permission.NewLoader(db)
	sessions := access.NewStore(db, "test-session-key", time.Hour)
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

// saBody 合法 SA 提交载荷（L1=S01 ⇒ expense_sales 最简归线）。
// ★ occurrence_period 须合 `conventions.field_payload_forms` 的 `iso_interval` 契约
// （`YYYY-MM-DD/YYYY-MM-DD`，闭区间）—— 原夹具写 `"2026-10-01 ~ 2026-10-31"`（`~` 分隔），
// 是**契约之前**的自由文本形态；`cross_month_allocation` 翻 `hard`（`N-054` ① 收尾）后
// 该形态被判「不可解析」⇒ 本夹具同批改为契约形态（★ 同月 ⇒ 不跨月 ⇒ S1 仍 200）。
func saBody(mut func(m map[string]any)) string {
	fields := map[string]any{
		"expense_subject":      "季度市场推广",
		"usage_category_l1":    "S01",
		"usage_category_l2":    "市场推广",
		"amount_cents":         50000,
		"purpose":              "秋季推广活动费用",
		"occurrence_period":    "2026-10-01/2026-10-31",
		"payment_method_input": "对公直付",
	}
	if mut != nil {
		mut(fields)
	}
	b, _ := json.Marshal(map[string]any{
		"doc_type":          "SA",
		"approval_code":     "code-sa",
		"usage_category_l1": fields["usage_category_l1"],
		"usage_category_l2": fields["usage_category_l2"],
		"fields":            fields,
	})
	return string(b)
}

// S1：合规 SA ⇒ 200 ＋ biz_no 前缀 SA- ＋ 实例/任务可读。
func TestN052SASubmitEndToEnd(t *testing.T) {
	e, db, auth := newSASubmitApp(t)
	cookie := auth.Establish("ou_app")

	code, env := postSubmit(t, e, cookie, saBody(nil), "")
	if code != http.StatusOK {
		t.Fatalf("S1 合规 SA 应 200，实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	biz, _ := d["biz_no"].(string)
	if !strings.HasPrefix(biz, "SA-") {
		t.Fatalf("S1 biz_no 应 SA- 前缀，实为 %q", biz)
	}
	// 实例可读（链算任务已建 —— 取实例可读即算，不推到终态）
	rows, err := db.ListFlowTasks(context.Background(), biz)
	if err != nil || len(rows) == 0 {
		t.Fatalf("S1 flow 任务应已创建：err=%v n=%d", err, len(rows))
	}
}

// S2：amount 负数 ⇒ 400 点名「金额必须大于 0」（★ 负数过结构化 required、
// 由真 spec 的 SA#amount_positive（severity=hard）拦截 —— 这一例即证明 SA 提交
// 路径真的执行了真 spec 的 hard 判据）。
// ★ =0 / 缺失：被 validateSubmitForm 的顶层 required **先拦**（providedNonEmpty
// 把 0/缺视为未填 ⇒ 文案「字段「申请金额」（amount_cents）必填」）—— 结构化与
// hard 对同一字段双把守、结构化在前（handler 顺序），hard 版对这两种形态恒不可达；
// 两形态仍 400 且点名金额（文案不同句，如实断言现状）。
func TestN052SAAmountPositiveHard(t *testing.T) {
	e, _, auth := newSASubmitApp(t)
	cookie := auth.Establish("ou_app")

	// ① 负数 ⇒ 结构化放行（非空）、hard 拦 —— 精确 hard 文案
	code, env := postSubmit(t, e, cookie, saBody(func(m map[string]any) { m["amount_cents"] = -5 }), "")
	if code != http.StatusBadRequest {
		t.Fatalf("S2 负数应 400，实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "金额必须大于 0") {
		t.Errorf("S2 负数文案应点名「金额必须大于 0」（SA#amount_positive hard 执行）：%s", env.Message)
	}
	// ② = 0 ⇒ 结构化先拦（仍 400 点名金额）
	code, env = postSubmit(t, e, cookie, saBody(func(m map[string]any) { m["amount_cents"] = 0 }), "")
	if code != http.StatusBadRequest || !strings.Contains(env.Message, "金额") {
		t.Errorf("S2 =0 应 400 且点名金额：%d %s", code, env.Message)
	}
	// ③ 缺失 ⇒ 同 ②
	code, env = postSubmit(t, e, cookie, saBody(func(m map[string]any) { delete(m, "amount_cents") }), "")
	if code != http.StatusBadRequest || !strings.Contains(env.Message, "金额") {
		t.Errorf("S2 缺失应 400 且点名金额：%d %s", code, env.Message)
	}
}

// S3：L2 留空 ⇒ 400 点名 usage_category_l2。
// ★ 如实说明：结构化（SA#usage_category_l2 required）与 hard（SA#completeness_l2）
// 对同一输入双把守、validateSubmitForm 在前 ⇒ 现实文案＝「字段「二级明细」…
// 必填」；hard 版「用途分类一/二级均须填写」对空值形态**恒不可达**（等价冗余双保险）。
// hard 版的可执行性由 §1.3 覆盖钉子（编译期）＋ S2 负数例（hard 真拦）证明。
func TestN052SACompletenessL2Hard(t *testing.T) {
	e, _, auth := newSASubmitApp(t)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, saBody(func(m map[string]any) { m["usage_category_l2"] = "" }), "")
	if code != http.StatusBadRequest {
		t.Fatalf("S3 L2 空应 400，实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "usage_category_l2") && !strings.Contains(env.Message, "二级明细") {
		t.Errorf("S3 文案应点名 L2 字段：%s", env.Message)
	}
}

// S4：PR#qualification_doc 条件必填（validateSubmitForm / evalSimpleEqual 路径，三态）。
// ★ 与 S1–S3 分开：一个走结构化子集、一个走 evaluateHardChecks。
func TestN052PRQualificationDocConditional(t *testing.T) {
	e, _, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")
	// 合规 PR 基底（okRow 全字段）＋ safety 开关
	bodyWith := func(safety bool, qual string) string {
		m := map[string]any{}
		b, _ := json.Marshal(m)
		_ = b
		base := frontendShapePRBody(okRow, "")
		// 注入/覆盖 header 字段（fields 对象内）
		var payload map[string]any
		_ = json.Unmarshal([]byte(base), &payload)
		f := payload["fields"].(map[string]any)
		f["is_safety_or_special_equipment"] = safety
		if qual != "" {
			f["qualification_doc"] = qual
		} else {
			delete(f, "qualification_doc")
		}
		out, _ := json.Marshal(payload)
		return string(out)
	}

	// ① true ∧ 空 ⇒ 400 点名 qualification_doc
	code, env := postSubmit(t, e, cookie, bodyWith(true, ""), "")
	if code != http.StatusBadRequest {
		t.Fatalf("S4① true∧空 应 400，实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "qualification_doc") {
		t.Errorf("S4① 文案应点名 qualification_doc：%s", env.Message)
	}
	// ② true ∧ 有值 ⇒ 200
	code, env = postSubmit(t, e, cookie, bodyWith(true, "资质文件 A"), "")
	if code != http.StatusOK {
		t.Fatalf("S4② true∧有值 应 200，实为 %d（%s）", code, env.Message)
	}
	// ③ false ∧ 空 ⇒ 200（条件未命中 ⇒ 放的钉子）
	code, env = postSubmit(t, e, cookie, bodyWith(false, ""), "")
	if code != http.StatusOK {
		t.Fatalf("S4③ false∧空 应 200（不反向），实为 %d（%s）", code, env.Message)
	}
}

// ---- §1.3 注册表覆盖钉子（编译/测试期把「缝」从运行期挪出来，单据无关）----

func TestSubmitHardChecksCoverRealSpec(t *testing.T) {
	bundle := metaTestBundle(t)
	skip := map[string]bool{"idempotency_key": true, "contract_no_format": true}
	missing := []string{}
	for dt, form := range bundle.Forms {
		for _, c := range form.Checks {
			if c.Severity != "hard" {
				continue
			}
			when := strings.TrimSpace(c.When)
			isSubmit := when == "submit" || strings.HasPrefix(when, "submit ") || strings.Contains(when, "提交前")
			if !isSubmit || skip[c.ID] {
				continue
			}
			if _, ok := submitHardChecks[c.ID]; !ok {
				missing = append(missing, dt+"#"+c.ID)
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("提交时点 hard 判据未注册求值器（该单据无提交用例时 evaluateHardChecks 不会兜住 —— 编译期钉子）：%s", m)
	}
}
