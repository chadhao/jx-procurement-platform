package flow_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// 本文件原为 **QA 独立复核（Batch Q）** 新增（`/tmp/qa_batchQ_jxp_independent_test.go`），
// 目的：用反向情形 / 边界 / 变异验证 Batch A-1/A-2 的声明，而非复跑实现者用例。
// 由 `software-engineer-3` 于 **Batch A-5（#46）** 收编进仓库：**用例名不改、断言不删**。
//
// ★ 收编适配（仅 3 处，均因本仓已并入 A-3 的 S7「提交前定义存在校验」；QA 原文写于无 S7 的副本上）：
//  1. 6 处 `storetest.NewDB(t)` → `newFlowDB(t)`（本包测试基建，预置 `code-pr → PR` 定义）。
//     原因：S7 下未注册 def 的 approval_code 会被**可见拒绝**（ErrDefinitionMissing），
//     裸库提交根本走不到被测路径；`newFlowDB` 使提交路径贴近生产。
//  2. `qaFinish` 的 `ApprovalCode: "code-" + docType`（= "code-PR"）→ `"code-pr"`：
//     复用上述**唯一**预置定义；本文件仅用 docType=PR，语义等价。
//  3. 新增 `TestQAInsertAuditTxLands`（team-lead #46 要求）：为第三层自检的**审计机制**
//     补一条「确实落行」的断言（QA 的注入探针发现过「注入不落行」）。
// 其余（跨实例隔离 / 顺序会签不变量 8 步穿插复算 =1 / 第三层自检缺口）**逐字保留**。

// qaReleasedPending 复算 04a §2.3 不变量：实例「可办理」(RELEASED 且 PENDING) 任务数。
func qaReleasedPending(t *testing.T, db *store.DB, bizNo string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_flow_task WHERE biz_no = ? AND release_state='RELEASED' AND status='PENDING'`,
		bizNo).Scan(&n); err != nil {
		t.Fatalf("复算不变量失败: %v", err)
	}
	return n
}

// qaSubmitQ 提交 3 人同节点 + 1 人第二节点 的链（用于跨实例/不变量测试）。
func qaSubmitQ(t *testing.T, svc *flow.Service, bizTag string) string {
	t.Helper()
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app_" + bizTag, ApplicantName: "申请人",
		Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "会签组", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_a", Name: "A"}, {OpenID: "ou_b", Name: "B"}, {OpenID: "ou_c", Name: "C"}}},
			{NodeID: "n2", NodeName: "终审", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_d", Name: "D"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

// isTerm 判定实例是否终态。
func isTerm(status string) bool {
	switch status {
	case flow.InstanceApproved, flow.InstanceRejected, flow.InstanceCanceled:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// ① 顺序会签不变量 + 跨实例隔离（含 task_id 含 biz_no 的 P0 回归）
// ---------------------------------------------------------------------------

// TestQAInvariantAndCrossInstanceIsolation：
//
//	(a) 提交后每个实例「可办理」恰 1 个；
//	(b) 两个实例**交错**推进，每步复算仍恰 1 个；
//	(c) 跨实例隔离：推进 I1 不得影响 I2 的任务释放/状态；
//	(d) ★ P0 回归：两实例同 node 同 assignee，各自都必须有自己的任务（task_id 含 biz_no）。
func TestQAInvariantAndCrossInstanceIsolation(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	b1 := qaSubmitQ(t, svc, "1")
	b2 := qaSubmitQ(t, svc, "2")

	// (d) 两实例任务集：各 4 个，且 task_id 全不相交（P0：task_id 不含 biz_no 时会撞 PK 静默不建）。
	t1, err := db.ListFlowTasks(ctx, b1)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := db.ListFlowTasks(ctx, b2)
	if err != nil {
		t.Fatal(err)
	}
	if len(t1) != 4 {
		t.Fatalf("实例1 任务数 = %d, 期望 4（P0：task_id 撞 PK 会静默少建）", len(t1))
	}
	if len(t2) != 4 {
		t.Fatalf("实例2 任务数 = %d, 期望 4（P0：两实例同 node 同 assignee）", len(t2))
	}
	seen := map[string]int{}
	for _, tk := range append(append([]store.FlowTask{}, t1...), t2...) {
		seen[tk.TaskID]++
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("task_id %q 出现 %d 次（跨实例撞 PK）", id, c)
		}
	}

	// (a) 提交后不变量。
	for _, b := range []string{b1, b2} {
		if got := qaReleasedPending(t, db, b); got != 1 {
			t.Fatalf("提交后实例 %s 可办理任务 = %d, 期望 1", b, got)
		}
	}

	// (b)+(c) 交错推进；每步复算不变量，并核对对端未被扰动。
	steps := []struct{ biz, assignee string }{
		{b1, "ou_a"}, {b2, "ou_a"},
		{b1, "ou_b"}, {b2, "ou_b"},
		{b1, "ou_c"}, {b2, "ou_c"},
		{b1, "ou_d"}, {b2, "ou_d"},
	}
	for _, s := range steps {
		tk := taskFor(t, db, s.biz, s.assignee)
		if err := svc.Approve(ctx, s.biz, tk.TaskID, s.assignee, "同意"); err != nil {
			t.Fatalf("同意 %s/%s 失败: %v", s.biz, s.assignee, err)
		}
		other := b1
		if s.biz == b1 {
			other = b2
		}
		// ★ 严格不变量：非终态实例「可办理」**恰 = 1**（终态 = 0）。
		want := 1
		if isTerm(instOf(t, db, s.biz).Status) {
			want = 0
		}
		if got := qaReleasedPending(t, db, s.biz); got != want {
			t.Errorf("推进 %s/%s 后不变量破坏：RELEASED+PENDING = %d, 期望 %d", s.biz, s.assignee, got, want)
		}
		// 隔离：对端不变量亦须满足（推进一端不得扰动另一端）。
		wantOther := 1
		if isTerm(instOf(t, db, other).Status) {
			wantOther = 0
		}
		if got := qaReleasedPending(t, db, other); got != wantOther {
			t.Errorf("推进 %s 扰动了另一端 %s：不变量 = %d, 期望 %d", s.biz, other, got, wantOther)
		}
	}

	// 终态后不变量 = 0。
	for _, b := range []string{b1, b2} {
		if got := instOf(t, db, b).Status; got != flow.InstanceApproved {
			t.Errorf("实例 %s 终态 = %s, 期望 APPROVED", b, got)
		}
		if got := qaReleasedPending(t, db, b); got != 0 {
			t.Errorf("终态实例 %s 仍有 %d 个可办理任务", b, got)
		}
	}
}

// TestQAHeldApproveRejectedAcrossInstances：隔离下，给「尚未轮到」的 HELD 任务 approve 必须被拒，
// 且**不得**推进实例（这是 §2.3 会签语义的直接断言）。
func TestQAHeldApproveRejectedAcrossInstances(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	b := qaSubmitQ(t, svc, "h")

	// ou_b 为 HELD（ou_a 未批）。
	tb := taskFor(t, db, b, "ou_b")
	if tb.ReleaseState != "HELD" {
		t.Fatalf("ou_b 初始 release_state = %s, 期望 HELD", tb.ReleaseState)
	}
	err := svc.Approve(ctx, b, tb.TaskID, "ou_b", "抢跑")
	if err == nil {
		t.Fatalf("HELD 任务 approve 未被拒（静默放行）")
	}
	// ★ 注意：实现以 fmt.Errorf("%w: ...", ErrTaskHeld, ...) 包装，故 err.Error() 并**不含** "HELD"
	//   字面量，且不等于 ErrTaskHeld.Error()。唯一正确判据是 errors.Is。
	if !errors.Is(err, flow.ErrTaskHeld) {
		t.Errorf("拒因非 ErrTaskHeld：%v", err)
	}
	if got := instOf(t, db, b).Status; got != flow.InstancePending {
		t.Errorf("HELD 抢跑后实例 = %s, 期望 PENDING", got)
	}
	// ou_b 仍 HELD+PENDING，未被误改。
	tb2 := taskFor(t, db, b, "ou_b")
	if tb2.ReleaseState != "HELD" || tb2.Status != flow.TaskPending {
		t.Errorf("抢跑改了任务状态: release=%s status=%s", tb2.ReleaseState, tb2.Status)
	}
}

// TestQARepushDoesNotResetRelease ★ 落后快照重推（同 task_id、HELD/PENDING）不得把
// 已 RELEASED/APPROVED/REJECTED 的任务置回 HELD（「被撞回起点」静默缺陷）。
func TestQARepushDoesNotResetRelease(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	b := qaSubmitQ(t, svc, "rp")

	ta := taskFor(t, db, b, "ou_a") // RELEASED
	if err := svc.Approve(ctx, b, ta.TaskID, "ou_a", "同意"); err != nil {
		t.Fatal(err)
	}
	before := taskFor(t, db, b, "ou_a")
	if before.Status != flow.TaskApproved {
		t.Fatalf("ou_a = %s, 期望 APPROVED", before.Status)
	}

	// 直接以「落后快照」重放该 task 行（RELEASED/PENDING 是提交时值）。
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		return db.UpsertFlowTaskTx(ctx, tx, &store.FlowTask{
			TaskID: ta.TaskID, BizNo: b, NodeID: "n1", NodeName: "会签组",
			NodeSeq: 1, Round: 1, AssigneeOpenID: "ou_a",
			Status: flow.TaskPending, ReleaseState: "HELD", TaskOrder: 1,
			CreatedAt: flowAt, UpdatedAt: flowAt,
		})
	}); err != nil {
		t.Fatal(err)
	}
	after := taskFor(t, db, b, "ou_a")
	if after.Status != flow.TaskApproved || after.ReleaseState == "HELD" {
		t.Errorf("重推把已推进任务置回：status=%s release=%s（期望 APPROVED / 非 HELD）",
			after.Status, after.ReleaseState)
	}
}

// ---------------------------------------------------------------------------
// ② finalize 覆盖 + 第三层自检
// ---------------------------------------------------------------------------

// qaFinish 提交并审批到 APPROVED（单节点单人）。
func qaFinish(t *testing.T, db *store.DB, svc *flow.Service, docType string, fields map[string]any) string {
	t.Helper()
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: docType, ApprovalCode: "code-pr", // 收编适配：原为 "code-"+docType，见文件头说明
		ApplicantOpenID: "ou_app",
		BizFields:       fields,
		Nodes:           []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:              flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	tk := taskFor(t, db, bizNo, "ou_x")
	if err := svc.Approve(context.Background(), bizNo, tk.TaskID, "ou_x", "同意"); err != nil {
		t.Fatalf("同意失败: %v", err)
	}
	return bizNo
}

func qaLedgerCount(t *testing.T, db *store.DB, lt, bizNo string) int {
	t.Helper()
	return storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type = ? AND biz_no = ?`, lt, bizNo)
}

// TestQAThirdLayerSelfCheckIsTautological ★★ 核心负向：finalize 的「落账后自检」只核对
// **它自己刚写的 write 集合**，因此在正常路径下**永不可能触发**（自证）。
// 本用例证明：正常落账 → 无任何 finalize_missing_ledger 审计行。
func TestQAThirdLayerSelfCheckIsTautological(t *testing.T) {
	db := newFlowDB(t)
	maps := &config.Maps{Ledger: map[string][]string{"PR": {"L02", "L03"}}}
	svc := flow.NewWithConfig(db, "app", maps, nil)
	bizNo := qaFinish(t, db, svc, "PR", nil)

	if qaLedgerCount(t, db, "L02", bizNo) != 1 || qaLedgerCount(t, db, "L03", bizNo) != 1 {
		t.Fatalf("正常路径应落 L02/L03 各 1 行")
	}
	n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_audit_log WHERE action='finalize_missing_ledger' AND target_id = ?`, bizNo)
	if n != 0 {
		t.Errorf("正常落账却产生 %d 条 finalize_missing_ledger 审计行", n)
	}
}

// TestQAThirdLayerSelfCheckMissesOmittedConfig ★★ 真正的 B47 缺口：若某类**应存在**的实例级台账
// 没被配置进 doc_type 映射，finalize 根本不知道它「应有」→ 不写、不检查、不告警 → 界面恒 0。
// 这正是 B47「恒 0 恰等于期望值」的未覆盖形态。
func TestQAThirdLayerSelfCheckMissesOmittedConfig(t *testing.T) {
	db := newFlowDB(t)
	// ★ 故意只配 L02，漏掉 L03（生产上 L03 = 采购经办登记台账）。
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	maps := &config.Maps{Ledger: map[string][]string{"PR": {"L02"}}}
	svc := flow.NewWithConfig(db, "app", maps, logger)
	bizNo := qaFinish(t, db, svc, "PR", nil)

	if qaLedgerCount(t, db, "L02", bizNo) != 1 {
		t.Fatalf("L02 应落 1 行")
	}
	// L03 无行 —— 且没有任何自检/告警把它标出来（这就是缺口）。
	if got := qaLedgerCount(t, db, "L03", bizNo); got != 0 {
		t.Fatalf("L03 行数 = %d，前提不成立", got)
	}
	if strings.Contains(buf.String(), "自检") || strings.Contains(buf.String(), "缺失") {
		t.Errorf("缺配置竟被自检捕获（与预期相反）: %s", buf.String())
	}
	auditN := storetest.Count(t, db, `SELECT COUNT(*) FROM t_audit_log WHERE action='finalize_missing_ledger'`)
	if auditN != 0 {
		t.Errorf("缺配置产生了 %d 条审计（应为 0：finalize 只核对它自己写的集合）", auditN)
	}
	t.Logf("★ 缺口确认：L03 未配置 → 无行、无自检、无审计、无告警（B47 类静默未覆盖）")
}

// TestQAFinalizeHoldsL08L10L11L12：落账目标硬拦（端到端，非纯函数）。
func TestQAFinalizeHoldsL08L10L11L12(t *testing.T) {
	db := newFlowDB(t)
	maps := &config.Maps{Ledger: map[string][]string{"PR": {"L02", "L08", "L10", "L11", "L12"}}}
	svc := flow.NewWithConfig(db, "app", maps, nil)
	bizNo := qaFinish(t, db, svc, "PR", nil)

	if qaLedgerCount(t, db, "L02", bizNo) != 1 {
		t.Errorf("L02 应落行")
	}
	for _, lt := range []string{"L08", "L10", "L11", "L12"} {
		if got := qaLedgerCount(t, db, lt, bizNo); got != 0 {
			t.Errorf("非实例级台账 %s 被落行 %d 次（应硬拦）", lt, got)
		}
	}
}

// TestQAInsertAuditTxLands ★ 收编新增（#46）：第三层自检的**审计机制**必须真的落行。
// QA 的注入探针曾发现「注入不落行」（事务内用了走 *sql.DB 的 InsertAudit → 单连接自锁）。
// 本断言把「InsertAuditTx 在事务内确实写入 t_audit_log」钉死。
func TestQAInsertAuditTxLands(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		return db.InsertAuditTx(ctx, tx, &store.AuditLogRow{
			Action: "qa_insert_audit_probe", Resource: "t_audit_log",
			TargetID: "PROBE-1", Result: "warn",
		})
	}); err != nil {
		t.Fatalf("事务内 InsertAuditTx 失败: %v", err)
	}
	n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_audit_log WHERE action='qa_insert_audit_probe' AND target_id='PROBE-1'`)
	if n != 1 {
		t.Errorf("★ InsertAuditTx 未落行：audit 行数 = %d（事务内审计机制失效）", n)
	}
}

// ---------------------------------------------------------------------------
// ③ R17 write-once / R18 终态守卫 的边界
// ---------------------------------------------------------------------------

func qaUpsert(t *testing.T, db *store.DB, in *store.Instance) error {
	t.Helper()
	return db.UpsertInstance(context.Background(), in)
}

func qaGetRaw(t *testing.T, db *store.DB, code string) (status, appID, appName, dept, extJSON string, amount sql.NullInt64) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(),
		`SELECT status, COALESCE(applicant_open_id,''), COALESCE(applicant_name,''),
		        COALESCE(department,''), COALESCE(ext_json,''), amount_cents
		 FROM t_instance WHERE instance_code = ?`, code).
		Scan(&status, &appID, &appName, &dept, &extJSON, &amount); err != nil {
		t.Fatalf("回读 %s 失败: %v", code, err)
	}
	return
}

// TestQAR18TerminalNoRegression：APPROVED 被「旧链式写入」改回 PENDING → 必须被拒。
func TestQAR18TerminalNoRegression(t *testing.T) {
	db := storetest.NewDB(t)
	code := "app:PR-2609-0001"
	if err := qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0001",
		Status: "APPROVED", Source: "flow", CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatal(err)
	}
	// 旧链式写入：doc_type/biz_no 同、status=PENDING。
	if err := qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0001",
		Status: "PENDING", Source: "event", CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatal(err)
	}
	st, _, _, _, _, _ := qaGetRaw(t, db, code)
	if st != "APPROVED" {
		t.Errorf("R18 失守：APPROVED 被改回 %s", st)
	}
}

// TestQAR18EmptyStatusBlanksPending ★ 边界：非终态实例被写入空 status（”）时会被置空？
func TestQAR18EmptyStatusBlanksPending(t *testing.T) {
	db := storetest.NewDB(t)
	code := "app:PR-2609-0002"
	if err := qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0002",
		Status: "PENDING", Source: "flow", CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatal(err)
	}
	_ = qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0002",
		Status: "", Source: "event", CreatedAt: flowAt, UpdatedAt: flowAt,
	})
	st, _, _, _, _, _ := qaGetRaw(t, db, code)
	if st == "" {
		t.Errorf("★ 边界缺陷：非终态实例被空 status 写入后 status 变空（静默污染）")
	} else {
		t.Logf("空 status 写入后 status=%q（未置空）", st)
	}
}

// TestQAR17WriteOnce：三列非空后被覆盖 → 必须保留；空→有值时应填充。
func TestQAR17WriteOnce(t *testing.T) {
	db := storetest.NewDB(t)
	code := "app:PR-2609-0003"
	if err := qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0003",
		Status: "PENDING", ApplicantOpenID: "ou_orig", ApplicantName: "原始", Department: "生产部",
		Source: "flow", CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatal(err)
	}
	// 后到报文的异构身份：非空但不同 → 必须**不覆盖**。
	if err := qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0003",
		Status: "PENDING", ApplicantOpenID: "ou_other", ApplicantName: "他人", Department: "采购部",
		Source: "event", CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatal(err)
	}
	_, appID, appName, dept, _, _ := qaGetRaw(t, db, code)
	if appID != "ou_orig" || appName != "原始" || dept != "生产部" {
		t.Errorf("R17 失守：身份/部门被覆盖为 %q/%q/%q", appID, appName, dept)
	}
}

// TestQAExtJSONBoundary ★ 边界：R19 声称「ext_json 空不覆盖」，但 Go 侧把 "" 归一为 "{}"，
// 而 "{}" 是**非空**值 → 会不会把已有有效 ext_json 抹成 "{}"？
func TestQAExtJSONBoundary(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	code := "app:PR-2609-0004"
	if err := qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0004",
		Status: "PENDING", ExtJSON: `{"contract_no":"CT-2609-0009"}`, Source: "flow",
		CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatal(err)
	}
	// 后到写入：ExtJSON 为空（Go 侧 defaultStr → "{}"）。
	if err := qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0004",
		Status: "PENDING", ExtJSON: "", Source: "event", CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatal(err)
	}
	var ext string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(ext_json,'') FROM t_instance WHERE instance_code=?`, code).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ext, "contract_no") {
		t.Logf("ext_json 被保留: %s", ext)
	} else {
		t.Errorf("★ 边界缺陷：ExtJSON=\"\" 经 Go 的 defaultStr→\"{}\" 覆盖，把有效 ext_json 抹成 %q", ext)
	}

	// 再测：显式 "   "（空白），以及直接 SQL 写 '' 是否保留。
	_ = qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0004",
		Status: "PENDING", ExtJSON: "   ", Source: "event", CreatedAt: flowAt, UpdatedAt: flowAt,
	})
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(ext_json,'') FROM t_instance WHERE instance_code=?`, code).Scan(&ext)
	t.Logf("空白 ExtJSON 写入后 ext_json=%q", ext)
}

// TestQAAmountZeroOverwrite：amount_cents 由非 0 被写入 0 → COALESCE 是否会覆盖？
func TestQAAmountZeroOverwrite(t *testing.T) {
	db := storetest.NewDB(t)
	code := "app:PR-2609-0005"
	amt := int64(12345)
	if err := qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0005",
		Status: "PENDING", AmountCents: &amt, Source: "flow", CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	_ = qaUpsert(t, db, &store.Instance{
		InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "PR-2609-0005",
		Status: "PENDING", AmountCents: &zero, Source: "event", CreatedAt: flowAt, UpdatedAt: flowAt,
	})
	_, _, _, _, _, got := qaGetRaw(t, db, code)
	if got.Valid && got.Int64 == 0 {
		t.Errorf("★ 边界：amount_cents 由 12345 被 0 覆盖（COALESCE 取非空 0）")
	} else {
		t.Logf("amount_cents 保留 = %v", got)
	}
}

var _ = sort.Strings

// TestQANullIfCoalesceMatrix ★ ③ 边界矩阵（纯文档化：逐格 dump）。对 **同一行**依次以
//
//	'' / '   '（空白） / '0' / NULL / '{}' 作「后到写入」，记录 t_instance 各列实际落值，
//	用来说明 NULLIF+COALESCE 在「空值」与「非空字面量」上的分界，以及 status/ext_json/amount
//	三处**不设 NULLIF 防护**的泄漏格。
func TestQANullIfCoalesceMatrix(t *testing.T) {
	cell := func(name string, in *store.Instance) {
		db := storetest.NewDB(t)
		code := "app:M-0001"
		base := int64(100)
		if err := qaUpsert(t, db, &store.Instance{
			InstanceCode: code, ApprovalCode: "c", DocType: "PR", BizNo: "M-0001",
			Status: "PENDING", ApplicantOpenID: "ou_orig", ApplicantName: "原", Department: "生产部",
			ExtJSON: `{"k":1}`, AmountCents: &base, Source: "flow", CreatedAt: flowAt, UpdatedAt: flowAt,
		}); err != nil {
			t.Fatal(err)
		}
		in.InstanceCode, in.ApprovalCode, in.DocType, in.BizNo = code, "c", "PR", "M-0001"
		in.Source, in.CreatedAt, in.UpdatedAt = "event", flowAt, flowAt
		_ = qaUpsert(t, db, in)
		st, aID, aName, dept, ext, amt := qaGetRaw(t, db, code)
		amtS := "NULL"
		if amt.Valid {
			amtS = fmt.Sprintf("%d", amt.Int64)
		}
		t.Logf("MATRIX %-14s → status=%-10q appID=%-10q appName=%-6q dept=%-8q ext=%-12q amount=%s",
			name, st, aID, aName, dept, ext, amtS)
	}
	zero := int64(0)
	cell(`status=''`, &store.Instance{Status: ""})
	cell(`status='   '`, &store.Instance{Status: "   "})
	cell(`status='0'`, &store.Instance{Status: "0"})
	cell(`三列都=''`, &store.Instance{Status: "PENDING", ApplicantOpenID: "", ApplicantName: "", Department: ""})
	cell(`三列都='   '`, &store.Instance{Status: "PENDING", ApplicantOpenID: "   ", ApplicantName: "  ", Department: "\t"})
	cell(`三列='0'`, &store.Instance{Status: "PENDING", ApplicantOpenID: "0", ApplicantName: "0", Department: "0"})
	cell(`ext_json=''`, &store.Instance{Status: "PENDING", ExtJSON: ""})
	cell(`ext_json='{}'`, &store.Instance{Status: "PENDING", ExtJSON: "{}"})
	cell(`amount=nil`, &store.Instance{Status: "PENDING"})
	cell(`amount=0`, &store.Instance{Status: "PENDING", AmountCents: &zero})
	cell(`NULL-ish 全空`, &store.Instance{Status: "PENDING"})
}
