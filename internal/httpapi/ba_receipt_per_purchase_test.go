package httpapi

// N-065 T2/T3：`BA#receipt_per_purchase` 端到端 —— applicant 待办生成、
// approval(return_receipt) 判据拦/放双向、未注册求值器可见失败。
//
// 时序（purchase_tier1）：submit ⇒ approve_petty_cash → disburse（顺序会签释放）
// ⇒ return_receipt（generates_task: true · 办理人＝申请人本人）⇒ 同意即终态。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// TestBAN065ReturnReceiptTaskAndJudgeE2E T2①②③ ＋ T3 拦/放双向（handler 级端到端）。
func TestBAN065ReturnReceiptTaskAndJudgeE2E(t *testing.T) {
	e, db, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	ctx := context.Background()

	// ---- 提交 ----
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("BA 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)

	// ---- ① t_flow_task 出现 return_receipt 且 actor＝申请人（ou_app）----
	tasks, err := db.ListFlowTasks(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	var rr *store.FlowTask
	for i := range tasks {
		if tasks[i].NodeID == "return_receipt" {
			rr = &tasks[i]
		}
	}
	if rr == nil {
		t.Fatalf("t_flow_task 缺 return_receipt 任务（generates_task 未消费？）: %+v", taskIDs(tasks))
	}
	if rr.AssigneeOpenID != "ou_app" {
		t.Errorf("return_receipt 办理人 = %q, 期望申请人 ou_app", rr.AssigneeOpenID)
	}

	// ---- 推进到 return_receipt 释放（不代批目标任务）----
	rrTask := driveUntilNode(t, e, db, auth, bizNo, "return_receipt", nil)
	if rrTask == nil {
		t.Fatal("return_receipt 未按顺序释放")
	}

	// ---- T3 拦：未回交凭据 ⇒ 400 且点名判据 ----
	code2, env2 := postApproveOne(t, e, auth, bizNo, "ou_app", rrTask.TaskID, map[string]any{})
	if code2 != http.StatusBadRequest {
		t.Fatalf("空凭据应拦：实为 %d（%s）", code2, env2.Message)
	}
	if !strings.Contains(env2.Message, "receipt_per_purchase") {
		t.Errorf("拦截须点名判据 BA#receipt_per_purchase, got: %s", env2.Message)
	}

	// ---- T3 放：回交凭据齐 ⇒ 200 → ③ 推进（末节点 ⇒ 终态 APPROVED）----
	code3, env3 := postApproveOne(t, e, auth, bizNo, "ou_app", rrTask.TaskID, map[string]any{
		"payment_receipt_no":   "PJ-2026-0001",
		"payment_receipt_file": "att://receipt-1",
	})
	if code3 != http.StatusOK {
		t.Fatalf("凭据齐应放行：实为 %d（%s）", code3, env3.Message)
	}
	inst, err := db.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Status != "APPROVED" {
		t.Errorf("return_receipt 为末节点，同意后应终态 APPROVED, 实为 %s", inst.Status)
	}
	if rr2 := taskByNode(t, db, bizNo, "return_receipt"); rr2 == nil || rr2.Status != "APPROVED" {
		t.Errorf("return_receipt 任务未落 APPROVED: %+v", rr2)
	}
}

// TestApprovalChecksFailClosedUnregisteredN065 T3 fail-closed 可见失败证据：
// 合成表单里「hard + approval(node) + 非 manual + 未注册」⇒ 报错点名，绝不静默跳过。
// ★ 对照：manual 同款 ⇒ 跳过不报（引擎不越权执行人工判据）。
func TestApprovalChecksFailClosedUnregisteredN065(t *testing.T) {
	inst := &store.Instance{DocType: "BA", BizNo: "BA-2610-0001", ApplicantOpenID: "ou_app"}
	ghost := specload.CheckDoc{
		ID: "ghost_approval_node_check", Severity: "hard",
		When: "approval(return_receipt)", CarriedByKind: "code",
	}
	ghostManual := specload.CheckDoc{
		ID: "ghost_manual_check", Severity: "hard",
		When: "approval(return_receipt)", CarriedByKind: "manual",
	}

	err := evaluateApprovalChecksFor(context.Background(),
		specload.FormDoc{Checks: []specload.CheckDoc{ghost}}, inst, nil, "return_receipt", "ou_app", Deps{})
	if err == nil {
		t.Fatal("未注册的 approval hard 判据必须可见失败（当前实现静默跳过 ⇒ 红）")
	}
	if !strings.Contains(err.Error(), "未注册求值器") || !strings.Contains(err.Error(), "ghost_approval_node_check") {
		t.Errorf("错误须点名判据与根因, got: %v", err)
	}

	// manual 对照：同节点同 severity，仅 carried_by_kind=manual ⇒ 引擎跳过、不报错。
	if err := evaluateApprovalChecksFor(context.Background(),
		specload.FormDoc{Checks: []specload.CheckDoc{ghostManual}}, inst, nil, "return_receipt", "ou_app", Deps{}); err != nil {
		t.Errorf("manual 承载的判据引擎应跳过（不 fail-closed）, got: %v", err)
	}
}

// taskIDs 待办任务 id 列表（失败诊断用）。
func taskIDs(tasks []store.FlowTask) []string {
	out := make([]string, 0, len(tasks))
	for _, tk := range tasks {
		out = append(out, tk.NodeID+":"+tk.Status+"/"+tk.ReleaseState)
	}
	return out
}

// taskByNode 取该节点任一任务（终态断言用；无则 nil）。
func taskByNode(t *testing.T, db *store.DB, bizNo, nodeID string) *store.FlowTask {
	t.Helper()
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tasks {
		if tasks[i].NodeID == nodeID {
			return &tasks[i]
		}
	}
	return nil
}
