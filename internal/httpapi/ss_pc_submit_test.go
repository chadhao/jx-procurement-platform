package httpapi

// N-043 · 批 4（A4）：SS / PC 提交通道 handler 级端到端（此前三级 HTTP 提交测试只覆盖 BA）。
//   T1 SS：正例出 biz_no(SS-YYMM-####) ＋ 终态落 L09(exception_type=独家采购)；
//         节点时点 SS×tech_opinion / SS×pgm_final 拦/放双向，*_by/at 断言**服务端权威值**。
//   T2 PC：正例 ＋ L04 查不到 fail-closed ＋ L09 6 列自检无告警；
//         PC×ledger_submit 只在 node4 拦（node3 tier_approval 不放行不拦 —— N-038-附三）。
//   装配范式＝handlers_approval_submit_m4_test（httptest + 真实 Deps），不另造范式。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	"github.com/labstack/echo/v4"
)

// newSSPCApp 装配（同 M4 范式）：code-ss/code-pc 映射与定义、Ledger SS/PC→L09、
// 五类审批人角色（sole_source / change 两链的全部 actor）。
func newSSPCApp(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator) {
	t.Helper()
	ctx := context.Background()
	db := storetest.NewDB(t)
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, m := range []store.ConfigMappingRow{
		{MapKind: "approval_code", MapKey: "code-ss", MapValue: "SS", DocType: "SS"},
		{MapKind: "approval_code", MapKey: "code-pc", MapValue: "PC", DocType: "PC"},
	} {
		if err := db.UpsertConfigMapping(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	apprMap, err := config.LoadApprovalMap(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	maps := &config.Maps{
		Approval: apprMap,
		Ledger:   map[string][]string{"SS": {"L09"}, "PC": {"L09"}},
	}
	for _, def := range []store.ApprovalDef{
		{ApprovalCode: "code-ss", DocType: "SS", Name: "单一来源理由书", GroupName: "test", UpdatedAt: time.Now()},
		{ApprovalCode: "code-pc", DocType: "PC", Name: "采购变更单", GroupName: "test", UpdatedAt: time.Now()},
	} {
		d := def
		if err := db.UpsertApprovalDef(ctx, &d); err != nil {
			t.Fatal(err)
		}
	}
	// 申请人 ＋ 五类角色（chain.json#roles 中文 label；验收人走 roleLabelOverride）
	users := []store.UserRole{
		{OpenID: "ou_app", Name: "申请人甲", Role: "申请人", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_p", Name: "采购员", Role: "采购经办人", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_tech", Name: "验收员", Role: "验收人", Department: "质检部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_sup", Name: "主管乙", Role: "主管领导", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_pgm", Name: "总经理", Role: "项目总经理", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
		{OpenID: "ou_ops", Name: "运营主管", Role: "综合运营主管", Department: "综合运营部", Active: true, UpdatedAt: time.Now()},
	}
	for _, u := range users {
		if err := db.UpsertUserRole(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	// related_pr_must_exist：已批准的 PR
	if err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "I-PR-2609-0100", ApprovalCode: "code-pr", DocType: "PR",
		BizNo: "PR-2609-0100", Status: "APPROVED", StatusRaw: "APPROVED",
		ApplicantOpenID: "ou_app", Department: "仓储部", Source: "flow",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// PC#contract_no：L04 合同行（injectPCFields 等值反查源）
	if err := db.UpsertArchive(ctx, db, &store.LedgerArchive{
		LedgerType: "L04", BizNo: "CT-2609-0100", InstanceCode: "I-CT-0100",
		SourceDocType: "CT", Department: "仓储部", ApplicantOpenID: "ou_app",
		AmountCents: int64Ptr(500000), Supplier: "原供应商甲",
		ExtJSON:   `{"related_biz_no":"PR-2609-0100","usage_category_l1":"P01"}`,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	bundle, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatal(err)
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

func int64Ptr(v int64) *int64 { return &v }

// ssSubmitBody SS 合规载荷（user required 6 项全填；uniqueness_basis 取非
// 「独家代理／授权」值 ⇒ 不触发 sole_source_attachment 附件判据）。
func ssSubmitBody() string {
	return `{"doc_type":"SS","approval_code":"code-ss","amount_cents":300000,` +
		`"fields":{` +
		`"procurement_subject":"进口离子交换树脂","planned_supplier":"某某国际贸易",` +
		`"related_biz_no":"PR-2609-0100",` +
		`"uniqueness_statement":"全球仅此一家生产商","uniqueness_basis":"全国唯一生产商",` +
		`"price_evidence":"单一来源询价函 att://q1","is_price_negotiated":true` +
		`}}`
}

// pcSubmitBody PC 合规载荷（user required：contract_no/change_type/change_reason；
// change_amount≠0 触发 change_amount_nonzero 判据的正向面）。
// extra 追加顶层字段（如伪造 contract_no）。
func pcSubmitBody(extra string) string {
	return `{"doc_type":"PC","approval_code":"code-pc","amount_cents":100000,` +
		`"fields":{` +
		`"contract_no":"CT-2609-0100","change_type":"价格变更",` +
		`"change_amount_cents":100000,"change_reason":"原材料涨价，重新核价"` +
		`}` + extra + `}`
}

// postApproveOne 单步同意（HTTP 层鉴权＝assignee 本人；fields 为审批时点填报）。
func postApproveOne(t *testing.T, e *echo.Echo, auth *access.Authenticator,
	bizNo, assignee, taskID string, fields map[string]any) (int, Envelope) {
	t.Helper()
	fb, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"task_id":%q,"opinion":"同意","fields":%s}`, taskID, string(fb))
	rec, env := doRequest(e, http.MethodPost, "/api/approval/"+bizNo+"/approve",
		auth.Establish(assignee), body)
	return rec.Code, env
}

// driveToTerminal 逐任务推进到终态（每个任务以 assignee 身份走 HTTP approve；
// fieldsByNode 按 node 提供必填填报）。返回终态前的最后一次 approve 结果供断言。
func driveToTerminal(t *testing.T, e *echo.Echo, db *store.DB, auth *access.Authenticator,
	bizNo string, fieldsByNode map[string]map[string]any) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 15; i++ {
		inst, err := db.GetInstanceByBizNo(ctx, bizNo)
		if err != nil {
			t.Fatal(err)
		}
		if inst.Status == "APPROVED" || inst.Status == "REJECTED" || inst.Status == "CANCELED" {
			return
		}
		tasks, err := db.ListFlowTasks(ctx, bizNo)
		if err != nil {
			t.Fatal(err)
		}
		progressed := false
		for _, tk := range tasks {
			if tk.Status != "PENDING" || tk.ReleaseState == "HELD" {
				continue // HELD＝顺序会签未释放，等下一轮
			}
			fields := fieldsByNode[tk.NodeID]
			if fields == nil {
				fields = map[string]any{}
			}
			code, env := postApproveOne(t, e, auth, bizNo, tk.AssigneeOpenID, tk.TaskID, fields)
			if code != http.StatusOK {
				t.Fatalf("推进节点 %s 失败: http=%d msg=%s", tk.NodeID, code, env.Message)
			}
			progressed = true
		}
		if !progressed {
			// 可能刚释放下一节点（循环再取）；两轮无进展即判定卡死
			if i > 1 {
				t.Fatalf("推进卡死（第 %d 轮无任务可推进，biz=%s）", i, bizNo)
			}
		}
	}
	t.Fatalf("推进超过轮次上限仍未终态（biz=%s）", bizNo)
}

// l09Row 读 L09 行 ext（终态断言）。
func l09Row(t *testing.T, db *store.DB, bizNo string) map[string]any {
	t.Helper()
	var extRaw string
	if err := db.QueryRowContext(context.Background(),
		`SELECT COALESCE(ext_json,'{}') FROM t_ledger_archive WHERE ledger_type='L09' AND biz_no=?`,
		bizNo).Scan(&extRaw); err != nil {
		t.Fatalf("L09 行不存在（终态未落账？）: %v", err)
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(extRaw), &m); err != nil {
		t.Fatalf("L09 ext 解析失败: %v", err)
	}
	return m
}

// ---------------- T1 · SS ----------------

// TestSSSubmitEndToEnd 正例：合法载荷 → 200(SS- 前缀) → instance 落库 →
// 推进到终态 → L09 落行且 exception_type=独家采购；tech_opinion_by/at 为服务端权威值。
func TestSSSubmitEndToEnd(t *testing.T) {
	e, db, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")

	code, env := postSubmit(t, e, cookie, ssSubmitBody(), "")
	if code != http.StatusOK {
		t.Fatalf("SS 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)
	if !strings.HasPrefix(bizNo, "SS-") {
		t.Fatalf("biz_no = %q, 期望 SS- 前缀（number_format: SS-YYMM-####）", bizNo)
	}
	// instance 落库
	inst, err := db.GetInstanceByBizNo(context.Background(), bizNo)
	if err != nil || inst.DocType != "SS" {
		t.Fatalf("t_instance 未落 SS 实例: %v %+v", err, inst)
	}

	// 推进到终态：tech_opinion 节点带值（**故意伪造 *_by —— 断言服务端覆盖**）；pgm_final 带值
	driveToTerminal(t, e, db, auth, bizNo, map[string]map[string]any{
		"tech_opinion": {
			"tech_opinion":    "技术评估符合使用要求",
			"tech_opinion_by": "ou_forged_client_value", // 客户端伪造：必须被服务端权威覆盖
			"tech_opinion_at": "2000-01-01T00:00:00Z",   // 同上
		},
		"pgm_final": {"pgm_final_opinion": "同意独家采购"},
	})
	if got := instOfSS(t, db, bizNo); got != "APPROVED" {
		t.Fatalf("终态 = %s, 期望 APPROVED", got)
	}
	// L09 落行 + exception_type=独家采购（提交期注入）
	ext := l09Row(t, db, bizNo)
	if ext["exception_type"] != "独家采购" {
		t.Errorf("L09 exception_type = %v, 期望 独家采购", ext["exception_type"])
	}
	// *_by/at 断言**服务端权威值**（≠ 客户端伪造值）
	if ext["tech_opinion_by"] != "ou_tech" {
		t.Errorf("tech_opinion_by = %v, 期望服务端带入的 assignee ou_tech（客户端伪造必须被覆盖）", ext["tech_opinion_by"])
	}
	if v, _ := ext["tech_opinion_at"].(string); v == "" || v == "2000-01-01T00:00:00Z" {
		t.Errorf("tech_opinion_at = %v, 期望服务端时刻（非客户端伪造值）", ext["tech_opinion_at"])
	}
}

func mustListFlowTasks(t *testing.T, db *store.DB, bizNo string) []struct {
	TaskID         string
	NodeID         string
	Status         string
	ReleaseState   string
	AssigneeOpenID string
} {
	t.Helper()
	ts, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("ListFlowTasks(%s): %v", bizNo, err)
	}
	out := make([]struct {
		TaskID         string
		NodeID         string
		Status         string
		ReleaseState   string
		AssigneeOpenID string
	}, 0, len(ts))
	for _, x := range ts {
		out = append(out, struct {
			TaskID         string
			NodeID         string
			Status         string
			ReleaseState   string
			AssigneeOpenID string
		}{TaskID: x.TaskID, NodeID: x.NodeID, Status: x.Status, ReleaseState: x.ReleaseState, AssigneeOpenID: x.AssigneeOpenID})
	}
	return out
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func instOfSS(t *testing.T, db *store.DB, bizNo string) string {
	t.Helper()
	inst, err := db.GetInstanceByBizNo(context.Background(), bizNo)
	if err != nil {
		t.Fatal(err)
	}
	return inst.Status
}

// TestSSNodeTechOpinionBidirectional T1.2 节点时点双向（POST approve）：
// 缺 tech_opinion ⇒ 400；填了 ⇒ 放行（by/at 服务端带入 —— 见正例断言）。
func TestSSNodeTechOpinionBidirectional(t *testing.T) {
	e, db, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, ssSubmitBody(), "")
	if code != http.StatusOK {
		t.Fatalf("提交: %d %s", code, env.Message)
	}
	bizNo, _ := mustData(t, env)["biz_no"].(string)

	// ★ M2 规则：actor=purchaser 的动作环节（submit_reason）不生成审批任务。
	//   （N-044 起 tier_chain 经 tier_expand 展开**会**生成审批任务——见 driveUntilNode。）
	if tk0 := pendingTaskByNode(t, db, bizNo, "submit_reason"); tk0 != nil {
		t.Error("submit_reason（purchaser 环节）不应生成审批任务（M2 规则）")
	}

	// ① 应拦：tech_opinion 空
	tk2 := pendingTaskByNode(t, db, bizNo, "tech_opinion")
	code, env = postApproveOne(t, e, auth, bizNo, tk2.AssigneeOpenID, tk2.TaskID, map[string]any{})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 tech_opinion 应 400, 实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "tech_opinion") {
		t.Errorf("400 文案应点名 tech_opinion: %s", env.Message)
	}
	if got := instOfSS(t, db, bizNo); got != "PENDING" {
		t.Errorf("拦下后实例仍应 PENDING, 实为 %s", got)
	}

	// ② 应放行：填值（by 传伪造值 —— 服务端覆盖由正例断言）
	code, env = postApproveOne(t, e, auth, bizNo, tk2.AssigneeOpenID, tk2.TaskID, map[string]any{
		"tech_opinion":    "符合使用要求",
		"tech_opinion_by": "ou_forged",
	})
	if code != http.StatusOK {
		t.Fatalf("填值应放行, 实为 %d（%s）", code, env.Message)
	}
}

// TestSSNodePgmFinalBidirectional T1.3：pgm_final 同理（缺 ⇒ 400；填 ⇒ 放行）。
func TestSSNodePgmFinalBidirectional(t *testing.T) {
	e, db, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, ssSubmitBody(), "")
	if code != http.StatusOK {
		t.Fatalf("提交: %d %s", code, env.Message)
	}
	bizNo, _ := mustData(t, env)["biz_no"].(string)

	// ★ N-044 展开后实际任务＝ tech_opinion → tier_chain 展开项 → pgm_final → ledger_and_report
	//   ⇒ 按实际生成的任务推进到 pgm_final（不再写死步数 —— T5 同步）。
	tk := driveUntilNode(t, e, db, auth, bizNo, "pgm_final", map[string]map[string]any{
		"tech_opinion": {"tech_opinion": "符合"},
	})
	if tk == nil {
		all, _ := db.ListFlowTasks(context.Background(), bizNo)
		ids := []string{}
		for _, x := range all {
			ids = append(ids, x.NodeID+"/"+x.Status+"/"+x.ReleaseState)
		}
		t.Fatalf("未能推进到 pgm_final；任务清单=%v", ids)
	}

	// ① 应拦：pgm_final 空
	if tk == nil {
		t.Fatal("pgm_final 无 PENDING 任务（前序节点未推完？）")
	}
	code, env = postApproveOne(t, e, auth, bizNo, tk.AssigneeOpenID, tk.TaskID, map[string]any{})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 pgm_final_opinion 应 400, 实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "pgm_final_opinion") {
		t.Errorf("400 文案应点名 pgm_final_opinion: %s", env.Message)
	}
	// ② 应放行
	code, env = postApproveOne(t, e, auth, bizNo, tk.AssigneeOpenID, tk.TaskID, map[string]any{
		"pgm_final_opinion": "同意独家采购",
	})
	if code != http.StatusOK {
		t.Fatalf("填值应放行, 实为 %d（%s）", code, env.Message)
	}
}

// ---------------- T2 · PC ----------------

// driveUntilNode 循环推进（N-044 展开后任务集变大 —— 按**实际生成的任务**推进），
// 直到 targetNode 出现 PENDING 任务；每步以 assignee 身份走 HTTP approve，
// 409（未到达/未轮到）视为顺序问题跳过而非失败。返回目标任务（超轮次返回 nil）。
func driveUntilNode(t *testing.T, e *echo.Echo, db *store.DB, auth *access.Authenticator,
	bizNo, targetNode string, fieldsByNode map[string]map[string]any) *store.FlowTask {
	t.Helper()
	for i := 0; i < 20; i++ {
		if tk := pendingTaskByNode(t, db, bizNo, targetNode); tk != nil {
			return tk
		}
		tasks, err := db.ListFlowTasks(context.Background(), bizNo)
		if err != nil {
			t.Fatal(err)
		}
		progressed := false
		for _, tk := range tasks {
			if tk.Status != "PENDING" || tk.ReleaseState == "HELD" || tk.NodeID == targetNode {
				continue
			}
			fields := fieldsByNode[tk.NodeID]
			if fields == nil {
				fields = map[string]any{}
			}
			code, _ := postApproveOne(t, e, auth, bizNo, tk.AssigneeOpenID, tk.TaskID, fields)
			if code == http.StatusOK {
				progressed = true
			}
			// 409＝未到达/未轮到（顺序会签）——跳过等下一轮，不视为失败
		}
		if !progressed && i >= 3 {
			break // 连续无进展（防死循环）
		}
	}
	return pendingTaskByNode(t, db, bizNo, targetNode)
}

// pendingTaskByNode 取某节点**已释放**（RELEASED）的 PENDING 任务（无则 nil）——
// HELD＝顺序会签未轮到，不可办理（否则 driveUntilNode 会拿未释放任务提前返回）。
func pendingTaskByNode(t *testing.T, db *store.DB, bizNo, nodeID string) *store.FlowTask {
	t.Helper()
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tasks {
		if tasks[i].NodeID == nodeID && tasks[i].Status == "PENDING" && tasks[i].ReleaseState == "RELEASED" {
			return &tasks[i]
		}
	}
	return nil
}

// TestPCEndToEnd 正例：L04 在册 → 提交 200 → 推进终态 → L09 落行
// （exception_type=采购变更 + change_chain 有值）＋ L09 6 列自检无 ledger_l09_column_missing 告警。
func TestPCEndToEnd(t *testing.T) {
	e, db, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")

	code, env := postSubmit(t, e, cookie, pcSubmitBody(""), "")
	if code != http.StatusOK {
		t.Fatalf("PC 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)
	if !strings.HasPrefix(bizNo, "PC-") {
		t.Fatalf("biz_no = %q, 期望 PC- 前缀", bizNo)
	}

	// 节点：submit_change(purchaser) → tier_judge(system,无任务) → tier_approval(sup→pgm 会签)
	//      → ledger_submit(ops，node4 必填 resubmitted_to_group_at)
	driveToTerminal(t, e, db, auth, bizNo, map[string]map[string]any{
		"ledger_submit": {"resubmitted_to_group_at": "2026-10-04"},
	})
	if got := instOfSS(t, db, bizNo); got != "APPROVED" {
		t.Fatalf("终态 = %s, 期望 APPROVED", got)
	}
	ext := l09Row(t, db, bizNo)
	if ext["exception_type"] != "采购变更" {
		t.Errorf("L09 exception_type = %v, 期望 采购变更", ext["exception_type"])
	}
	if ext["change_chain"] == nil {
		t.Errorf("L09 change_chain 缺失（注入生产者未落）: %v", ext)
	}
	if v, _ := ext["resubmitted_to_group_at"].(string); v != "2026-10-04" {
		t.Errorf("resubmitted_to_group_at = %v", ext["resubmitted_to_group_at"])
	}
	// L09 6 列自检：无 ledger_l09_column_missing 告警（生产者齐全）
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_audit_log WHERE action='ledger_l09_column_missing' AND target_id=?`,
		bizNo).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("L09 列自检告警 = %d 条, 期望 0（6 列生产者齐全）: ", n)
	}
}

// TestPCL04FailClosed T2.2：L04 查不到 ⇒ 端到端 400（fail-closed，不是静默注入垃圾）。
func TestPCL04FailClosed(t *testing.T) {
	e, _, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie,
		pcSubmitBody(`,"amount_cents":1`), "") // 无用；改换 contract_no
	_ = code
	_ = env
	// 换 contract_no 指向不存在的合同
	body := strings.Replace(pcSubmitBody(""), `"contract_no":"CT-2609-0100"`,
		`"contract_no":"CT-0000-0000"`, 1)
	code, env = postSubmit(t, e, cookie, body, "")
	if code != http.StatusBadRequest {
		t.Fatalf("L04 无记录应 400 fail-closed, 实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "合同台账") {
		t.Errorf("400 文案应点名合同台账（等值反查失败）: %s", env.Message)
	}
}

// TestPCLedgerSubmitNodeBidirectional T2.3：只在 node4 拦 ——
// node3 tier_approval 不因缺 resubmitted_to_group_at 被拦（N-038-附三 / filled_at_note）；
// node4 缺 ⇒ 400；node4 填 ⇒ 放行。
func TestPCLedgerSubmitNodeBidirectional(t *testing.T) {
	e, db, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, pcSubmitBody(""), "")
	if code != http.StatusOK {
		t.Fatalf("提交: %d %s", code, env.Message)
	}
	bizNo, _ := mustData(t, env)["biz_no"].(string)

	// ★ N-044 展开后实际任务＝ tier_approval 展开项（sup＋pgm，r15_max 采二档）→ ledger_submit。
	//   node3 tier_approval **不带** resubmitted_to_group_at ⇒ 必须逐个放行（不拦 —— N-038-附三）；
	//   推进到 node4 出现（不再写死步数 —— T5 同步）。
	tk3 := driveUntilNode(t, e, db, auth, bizNo, "tier_approval", nil)
	// tier_approval 可能还有同节点后续（会签第二人）：全部放行直到 ledger_submit 可达
	for i := 0; i < 4; i++ {
		if pendingTaskByNode(t, db, bizNo, "ledger_submit") != nil {
			break
		}
		tk := pendingTaskByNode(t, db, bizNo, "tier_approval")
		if tk == nil {
			break
		}
		code, env = postApproveOne(t, e, auth, bizNo, tk.AssigneeOpenID, tk.TaskID, map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("node3 tier_approval 不应因缺登记日期被拦（N-038-附三）: %d %s", code, env.Message)
		}
	}
	_ = tk3
	// node4 ledger_submit：缺 ⇒ 400
	tk4 := pendingTaskByNode(t, db, bizNo, "ledger_submit")
	if tk4 == nil {
		all, _ := db.ListFlowTasks(context.Background(), bizNo)
		ids := []string{}
		for _, x := range all {
			ids = append(ids, x.NodeID+"/"+x.Status+"/"+x.ReleaseState)
		}
		t.Fatalf("ledger_submit 无 PENDING 任务（前序未推完？）；任务=%v", ids)
	}
	code, env = postApproveOne(t, e, auth, bizNo, tk4.AssigneeOpenID, tk4.TaskID, map[string]any{})
	if code != http.StatusBadRequest {
		t.Fatalf("node4 缺 resubmitted_to_group_at 应 400, 实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "resubmitted_to_group_at") {
		t.Errorf("400 文案应点名字段: %s", env.Message)
	}
	// node4 填 ⇒ 放行
	code, env = postApproveOne(t, e, auth, bizNo, tk4.AssigneeOpenID, tk4.TaskID, map[string]any{
		"resubmitted_to_group_at": "2026-10-04",
	})
	if code != http.StatusOK {
		t.Fatalf("node4 填值应放行, 实为 %d（%s）", code, env.Message)
	}
}

// TestSSPCTierExpandTasksGenerated N-044 完成判据①：handler 级端到端可证
// tier_chain / tier_approval 经 tier_expand 真的生成审批任务（展开项
// SourceNodeID = <节点id>_<role>，T3 命名）。
func TestSSPCTierExpandTasksGenerated(t *testing.T) {
	e, db, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")

	// ---- SS：600000 分（采三档）⇒ tier_chain 展开 = supervisor（exclude_roles 剔 PGM）----
	ssBody := strings.Replace(ssSubmitBody(), `"amount_cents":300000`, `"amount_cents":600000`, 1)
	code, env := postSubmit(t, e, cookie, ssBody, "")
	if code != http.StatusOK {
		t.Fatalf("SS 提交: %d %s", code, env.Message)
	}
	ssBiz, _ := mustData(t, env)["biz_no"].(string)
	ssIDs := []string{}
	for _, x := range mustListFlowTasks(t, db, ssBiz) {
		ssIDs = append(ssIDs, x.NodeID)
	}
	if !containsStr(ssIDs, "tier_chain_supervisor") {
		t.Fatalf("SS 采三档 tier_chain 应展开 supervisor 任务（SourceNodeID=tier_chain_supervisor），实际任务=%v", ssIDs)
	}
	if containsStr(ssIDs, "tier_chain_project_general_manager") {
		t.Fatalf("SS tier_chain 展开不得含 PGM（exclude_roles 去重，全链 PGM 唯一=pgm_final），实际=%v", ssIDs)
	}

	// ---- PC：change 140000 / orig 500000（R-15 就高=采二档 500000）⇒ tier_approval 展开 sup+pgm ----
	pcs := strings.Replace(pcSubmitBody(""), `"amount_cents":100000`, `"amount_cents":700000`, 1)
	pcs = strings.Replace(pcs, `"change_amount_cents":100000`, `"change_amount_cents":140000`, 1)
	code, env = postSubmit(t, e, cookie, pcs, "")
	if code != http.StatusOK {
		t.Fatalf("PC 提交: %d %s", code, env.Message)
	}
	pcBiz, _ := mustData(t, env)["biz_no"].(string)
	pcIDs := []string{}
	for _, x := range mustListFlowTasks(t, db, pcBiz) {
		pcIDs = append(pcIDs, x.NodeID)
	}
	if !containsStr(pcIDs, "tier_approval_supervisor") || !containsStr(pcIDs, "tier_approval_project_general_manager") {
		t.Fatalf("PC 采三档 tier_approval 应展开 sup+pgm 两任务，实际任务=%v", pcIDs)
	}
}
