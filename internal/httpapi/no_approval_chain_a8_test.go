package httpapi

// N-056 · A8：登记型单据（GR/QC/RFQ/BJ · no_approval_chain）发起通路 handler 级端到端。
// 契约＝ spec/chain.json#conventions.no_approval_chain（提交即终态 / 链为空 /
// 落账按 doc_chains.ledger / 不新增推送分支）。
// E1 GR（L07 一行）· E2 QC · E3 RFQ · E4 BJ（不落账）· E5 反向（fail-closed ＋ 过宽检查）。

import (
	"context"
	"encoding/json"
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
	storetest "github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	"github.com/labstack/echo/v4"
)

// newA8SubmitApp 四登记型单据 ＋ BA（GR 的关联单需经 HTTP 提交以产生 flow 任务 ——
// no_approver_in_acceptance_group 对「取不到审批记录」可见失败）提交夹具。
// mutate 在装载 spec 后执行（E5 内存 mutator，spec 文件零改动）。
func newA8SubmitApp(t *testing.T, mutate func(*specload.Bundle)) (*echo.Echo, *store.DB, *access.Authenticator, string) {
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
	// approval_code ↔ doc_type：GR/QC/RFQ/BJ ＋ BA（关联源）
	for _, m := range []store.ConfigMappingRow{
		{MapKind: "approval_code", MapKey: "code-gr", MapValue: "GR", DocType: "GR"},
		{MapKind: "approval_code", MapKey: "code-qc", MapValue: "QC", DocType: "QC"},
		{MapKind: "approval_code", MapKey: "code-rfq", MapValue: "RFQ", DocType: "RFQ"},
		{MapKind: "approval_code", MapKey: "code-bj", MapValue: "BJ", DocType: "BJ"},
		{MapKind: "approval_code", MapKey: "code-ba", MapValue: "BA", DocType: "BA"},
	} {
		if err := db.UpsertConfigMapping(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	apprMap, err := config.LoadApprovalMap(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	// Ledger 映射（运行时台账来源＝配置映射；GR→L07、其余登记型不落账）
	maps := &config.Maps{Approval: apprMap, Ledger: map[string][]string{
		"GR": {"L07"}, "QC": {}, "RFQ": {}, "BJ": {}, "BA": {},
	}}
	for _, ad := range []store.ApprovalDef{
		{ApprovalCode: "code-gr", DocType: "GR", Name: "到货验收单", GroupName: "test", UpdatedAt: time.Now()},
		{ApprovalCode: "code-qc", DocType: "QC", Name: "来料检验报告", GroupName: "test", UpdatedAt: time.Now()},
		{ApprovalCode: "code-rfq", DocType: "RFQ", Name: "询价单", GroupName: "test", UpdatedAt: time.Now()},
		{ApprovalCode: "code-bj", DocType: "BJ", Name: "比价表", GroupName: "test", UpdatedAt: time.Now()},
		{ApprovalCode: "code-ba", DocType: "BA", Name: "采购报备单", GroupName: "test", UpdatedAt: time.Now()},
	} {
		cp := ad
		if err := db.UpsertApprovalDef(ctx, &cp); err != nil {
			t.Fatal(err)
		}
	}
	// 角色：申请人（提交）＋ 综合运营主管（BA 采一链 approve_petty_cash）
	for _, r := range []store.UserRole{
		{OpenID: "ou_app", Name: "申请人", Role: "申请人", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
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
	// RFQ 关联 PR（直插 —— related_pr_must_exist 只查实例存在，同 SS 先例）
	if err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "I-PR-2610-0001", ApprovalCode: "code-pr", DocType: "PR",
		BizNo: "PR-2610-0001", Status: "APPROVED", StatusRaw: "APPROVED",
		ApplicantOpenID: "ou_app", Department: "仓储部", Source: "flow",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// QC 关联 GR（直插 —— 只查 DocType=GR）
	if err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "I-GR-2610-0900", ApprovalCode: "code-gr", DocType: "GR",
		BizNo: "GR-2610-0900", Status: "APPROVED", StatusRaw: "APPROVED",
		ApplicantOpenID: "ou_app", Department: "仓储部", Source: "flow",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// ★ 同步 L07 archive 行（真实 GR 提交即终态会落账；QC 的 l07_inspection_
	// conclusion_written 落账后自检要求 related GR 在 L07 有行 —— 号段取 0900 避开
	// 生成器序列，E1 提交生成的 GR 号与之天然不同）。
	if err := db.UpsertArchive(ctx, db, &store.LedgerArchive{
		LedgerType: "L07", BizNo: "GR-2610-0900", InstanceCode: "I-GR-2610-0900",
		SourceDocType: "GR", Department: "仓储部", ApplicantOpenID: "ou_app",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
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
	// GR 的关联 BA：经 HTTP 提交（产生 flow 任务 —— 审批人回避判据要求「取到审批记录」）
	cookie := auth.Establish("ou_app")
	code, envBA := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("关联 BA 提交应 200（夹具前置），实为 %d（%s）", code, envBA.Message)
	}
	ba, _ := mustData(t, envBA)["biz_no"].(string)
	if !strings.HasPrefix(ba, "BA-") {
		t.Fatalf("关联 BA biz_no 应 BA- 前缀，实为 %q", ba)
	}
	return e, db, auth, ba
}

// ---- 载荷（必填与 hard 判据全部从 spec/forms/*.json 实读构造）----

// grBody 合格入库（非让步 ⇒ concession 免）＋ P01 组成员矩阵（qc+warehouse 有、ops 无）
// ＋ 成员非 ops 角色、非 BA 链审批人（避开回避）。
func a8GRPayload(baBiz string) string {
	f := map[string]any{
		"related_order_or_record_no": baBiz,
		"arrival_date":               "2026-10-01",
		"acceptance_group":           "P01",
		"member_purchaser":           "张三",
		"member_qc":                  "李四",
		"member_warehouse":           "王五",
		"received_quantity":          10,
		"acceptance_conclusion":      "合格入库",
		"arrival_docs":               "到货单.pdf",
	}
	b, _ := json.Marshal(map[string]any{"doc_type": "GR", "approval_code": "code-gr", "fields": f})
	return string(b)
}

// qcBody 抽检（sample_quantity>0）＋ 合格（defect 免）＋ 关联直插 GR。
func a8QCPayload() string {
	f := map[string]any{
		"related_biz_no":         "GR-2610-0900",
		"inspection_date":        "2026-10-02",
		"inspection_method":      "抽检",
		"specification":          "GB/T 1234",
		"inspection_result":      "合格",
		"inspection_report_file": "检验报告.pdf",
		"sample_quantity":        5,
	}
	b, _ := json.Marshal(map[string]any{"doc_type": "QC", "approval_code": "code-qc", "fields": f})
	return string(b)
}

// rfqBody 询比价（≥3 家、明细计数）＋ 关联直插 PR ＋ 截止晚于发出。
func a8RFQPayload() string {
	f := map[string]any{
		"related_biz_no":      "PR-2610-0001",
		"procure_method":      "询比价",
		"procurement_subject": "滤布询价",
		"tech_spec":           "1200mm 工业滤布",
		"quote_deadline":      "2026-10-20 18:00",
		"quote_validity_days": 30,
		"send_method":         "邮件",
		"send_date":           "2026-10-05",
		"invited_suppliers":   "甲公司\n乙公司\n丙公司",
		"invited_count":       3,
		"rfq_file":            "询价文件.pdf",
		"send_evidence":       "发送截图.png",
		"responded_count":     3,
	}
	b, _ := json.Marshal(map[string]any{"doc_type": "RFQ", "approval_code": "code-rfq", "fields": f})
	return string(b)
}

// bjBody 3 家有效报价（bj_checks_test 同款行格式）＋ 选定有效且技术符合。
func a8BJPayload() string {
	f := map[string]any{
		"procure_method":         "询比价",
		"procurement_subject":    "滤布比价",
		"quotes":                 "甲公司 | 10000 | 有效 | 技术符合\n乙公司 | 10200 | 有效 | 技术符合\n丙公司 | 10500 | 有效 | 技术符合",
		"all_quotes_independent": true,
		"technical_compliance":   "甲公司符合；乙公司符合；丙公司符合",
		"selected_supplier":      "甲公司",
		"selected_reason":        "报价最低且技术符合",
		"rfq_files":              "询价文件.pdf",
		"quote_files":            "报价单.pdf",
	}
	b, _ := json.Marshal(map[string]any{"doc_type": "BJ", "approval_code": "code-bj", "fields": f})
	return string(b)
}

// ---- E1–E4 正例 ----

func a8AssertTerminal(t *testing.T, e *echo.Echo, db *store.DB, auth *access.Authenticator, name, payload, prefix string, wantLedger bool) string {
	t.Helper()
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, payload, "")
	if code != http.StatusOK {
		t.Fatalf("%s 提交应 200，实为 %d（%s）", name, code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	if st, _ := d["status"].(string); st != "APPROVED" {
		t.Errorf("%s 响应 status=%v，期望 APPROVED（提交即终态）", name, d["status"])
	}
	biz, _ := d["biz_no"].(string)
	if !strings.HasPrefix(biz, prefix) {
		t.Fatalf("%s biz_no 应 %s 前缀，实为 %q", name, prefix, biz)
	}
	// ④ 零审批任务
	rows, err := db.ListFlowTasks(ctxA8(), biz)
	if err != nil || len(rows) != 0 {
		t.Errorf("%s t_flow_task 应 0 行，实为 %d（err=%v）", name, len(rows), err)
	}
	// ⑤ 实例终态
	inst, err := db.GetInstanceByBizNo(ctxA8(), biz)
	if err != nil || inst == nil {
		t.Fatalf("%s 实例读取失败：%v", name, err)
	}
	if inst.Status != "APPROVED" {
		t.Errorf("%s t_instance.status=%s，期望 APPROVED", name, inst.Status)
	}
	// ⑥ 落账
	ar, _, err := db.ListArchive(ctxA8(), store.LedgerFilter{BizNo: biz})
	if err != nil {
		t.Fatalf("%s 读台账失败：%v", name, err)
	}
	if wantLedger {
		if len(ar) != 1 {
			t.Fatalf("%s 应有且仅有 1 行台账，实为 %d", name, len(ar))
		}
		if ar[0].LedgerType != "L07" {
			t.Errorf("%s 台账类型=%s，期望 L07", name, ar[0].LedgerType)
		}
		if ar[0].BizNo != biz {
			t.Errorf("%s 台账 biz_no=%s 与单据 %s 对不上", name, ar[0].BizNo, biz)
		}
		t.Logf("E1 L07 行：ledger=%s biz=%s inst=%s doc=%s amount=%v ext=%.120s",
			ar[0].LedgerType, ar[0].BizNo, ar[0].InstanceCode, ar[0].SourceDocType, ar[0].AmountCents, ar[0].ExtJSON)
	} else {
		if len(ar) != 0 {
			t.Errorf("%s 不应落账，实为 %d 行", name, len(ar))
		}
	}
	return biz
}

func ctxA8() context.Context { return context.Background() }

// E1 GR：200 · APPROVED · GR- · 0 任务 · L07 一行。
func TestA8E1GRSubmitTerminal(t *testing.T) {
	e, db, auth, ba := newA8SubmitApp(t, nil)
	a8AssertTerminal(t, e, db, auth, "E1-GR", a8GRPayload(ba), "GR-", true)
}

// E2 QC：200 · APPROVED · 0 任务 · 不落账。
func TestA8E2QCSubmitTerminal(t *testing.T) {
	e, db, auth, _ := newA8SubmitApp(t, nil)
	a8AssertTerminal(t, e, db, auth, "E2-QC", a8QCPayload(), "QC-", false)
}

// E3 RFQ：200 · APPROVED · 0 任务 · 不落账。
func TestA8E3RFQSubmitTerminal(t *testing.T) {
	e, db, auth, _ := newA8SubmitApp(t, nil)
	a8AssertTerminal(t, e, db, auth, "E3-RFQ", a8RFQPayload(), "RFQ-", false)
}

// E4 BJ：200 · APPROVED · 0 任务 · 不落账。
func TestA8E4BJSubmitTerminal(t *testing.T) {
	e, db, auth, _ := newA8SubmitApp(t, nil)
	a8AssertTerminal(t, e, db, auth, "E4-BJ", a8BJPayload(), "BJ-", false)
}

// ---- E5 反向 ----

// E5(b) fail-closed：GR 的 no_approval_chain 内存置 false 且无 route ⇒ 400 可见失败
// （ErrRouteMissing —— 机读规格不完整，不静默当登记型）。
func TestA8E5GRFlagFalseFailsClosed(t *testing.T) {
	e, _, auth, ba := newA8SubmitApp(t, func(b *specload.Bundle) {
		g := b.Chain.DocChains["GR"]
		g.NoApprovalChain = false
		g.Route = ""
		b.Chain.DocChains["GR"] = g
	})
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, a8GRPayload(ba), "")
	if code != http.StatusBadRequest {
		t.Fatalf("E5(b) flag=false 且无 route 应 400，实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "no_approval_chain") && !strings.Contains(env.Message, "route") {
		t.Errorf("E5(b) 文案应点名机读规格不完整：%s", env.Message)
	}
}

// E5(a) 过宽检查：BA 的 doc_chains 条目内存置 no_approval_chain=true ⇒ BA 的
// ResolveRoute 分支**不读该键** ⇒ 仍走既有采一档链（≥1 任务，不获得零节点通路）。
func TestA8E5BAFlagTrueStillHasChain(t *testing.T) {
	e, db, auth, _ := newA8SubmitApp(t, func(b *specload.Bundle) {
		g := b.Chain.DocChains["BA"]
		g.NoApprovalChain = true // 人为置 true —— BA 分支不读该键
		b.Chain.DocChains["BA"] = g
	})
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("E5(a) BA 提交应 200（既有链路径），实为 %d（%s）", code, env.Message)
	}
	ba, _ := mustData(t, env)["biz_no"].(string)
	rows, err := db.ListFlowTasks(ctxA8(), ba)
	if err != nil || len(rows) == 0 {
		t.Fatalf("E5(a) BA 置 flag 后仍应生成链任务（≥1），实为 %d（err=%v）", len(rows), err)
	}
}
