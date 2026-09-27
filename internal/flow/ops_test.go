package flow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// submitOneNode 提交单节点（审批人按声明序）票据；返回 biz_no。
func submitOneNode(t *testing.T, svc *flow.Service, assignees ...string) string {
	t.Helper()
	aps := make([]flow.Approver, 0, len(assignees))
	for _, a := range assignees {
		aps = append(aps, flow.Approver{OpenID: a})
	}
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{{NodeID: "n1", NodeName: "审批", Seq: 1, Approvers: aps}},
		At:    flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

// submitTwoNodes 提交两节点（各 1 审批人）票据。
func submitTwoNodes(t *testing.T, svc *flow.Service, n1, n2 string) string {
	t.Helper()
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "主管", Seq: 1, Approvers: []flow.Approver{{OpenID: n1}}},
			{NodeID: "n2", NodeName: "终审", Seq: 2, Approvers: []flow.Approver{{OpenID: n2}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

// ---------- 转交 ----------

// TestTransferReassigns 转交：原任务 TRANSFERRED，追加同节点新任务（RELEASED），新审批人可继续推进。
func TestTransferReassigns(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitOneNode(t, svc, "ou_m1")

	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Transfer(ctx, bizNo, m1.TaskID, "ou_m1", "ou_m9", "接替人", "休假"); err != nil {
		t.Fatalf("转交失败: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskTransferred {
		t.Errorf("原任务状态 = %s, 期望 TRANSFERRED", got)
	}
	m9 := taskFor(t, db, bizNo, "ou_m9")
	if m9.NodeID != "n1" || m9.ReleaseState != flow.ReleaseReleased || m9.Status != flow.TaskPending {
		t.Errorf("新任务 = {node:%s rel:%s status:%s}, 期望 {n1 RELEASED PENDING}", m9.NodeID, m9.ReleaseState, m9.Status)
	}
	// 新审批人继续 → 节点通过 → 实例 APPROVED。
	if err := svc.Approve(ctx, bizNo, m9.TaskID, "ou_m9", "同意"); err != nil {
		t.Fatalf("新审批人同意失败: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("实例 = %s, 期望 APPROVED", got)
	}
}

// TestTransferNegatives 转交负向：非本人 / HELD 任务 / 终态 → 必须被拒。
func TestTransferNegatives(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)
	m1 := taskFor(t, db, bizNo, "ou_a")
	// 非本人。
	if err := svc.Transfer(ctx, bizNo, m1.TaskID, "ou_x", "ou_m9", "", ""); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非本人转交应 ErrNotAssignee，实际: %v", err)
	}
	// HELD 任务（ou_b 未释放）。
	b := taskFor(t, db, bizNo, "ou_b")
	if err := svc.Transfer(ctx, bizNo, b.TaskID, "ou_b", "ou_m9", "", ""); !errors.Is(err, flow.ErrTaskHeld) {
		t.Errorf("HELD 任务转交应 ErrTaskHeld，实际: %v", err)
	}
	// 终态。
	_ = svc.Cancel(ctx, bizNo, "ou_app", "撤回")
	if err := svc.Transfer(ctx, bizNo, m1.TaskID, "ou_a", "ou_m9", "", ""); !errors.Is(err, flow.ErrIllegalTransition) {
		t.Errorf("终态转交应 ErrIllegalTransition，实际: %v", err)
	}
}

// ---------- 加签 ----------

// TestAddSignAppendsToTail 加签：并入同 node、task_order 在队尾、先 HELD；原审批人须等到其后所有人同意。
func TestAddSignAppendsToTail(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitOneNode(t, svc, "ou_m1")

	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.AddSign(ctx, bizNo, m1.TaskID, "ou_m1", "ou_m9", "加签人", "补充意见"); err != nil {
		t.Fatalf("加签失败: %v", err)
	}
	m9 := taskFor(t, db, bizNo, "ou_m9")
	if m9.NodeID != "n1" {
		t.Errorf("加签任务 node_id = %s, 期望 n1（并入同 node）", m9.NodeID)
	}
	if m9.TaskOrder <= m1.TaskOrder {
		t.Errorf("加签 task_order=%d 未在队尾（原=%d）", m9.TaskOrder, m1.TaskOrder)
	}
	if m9.ReleaseState != flow.ReleaseHeld {
		t.Errorf("加签任务应先 HELD，实际 %s", m9.ReleaseState)
	}

	// 原审批人同意 → 节点未通过（加签未审）；加签任务被释放。
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意"); err != nil {
		t.Fatal(err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Fatalf("加签未审时实例 = %s, 期望 PENDING（原审批人不得独过）", got)
	}
	if got := taskFor(t, db, bizNo, "ou_m9").ReleaseState; got != flow.ReleaseReleased {
		t.Errorf("加签任务应被释放，实际 %s", got)
	}
	// 加签人同意 → 节点通过 → APPROVED。
	if err := svc.Approve(ctx, bizNo, m9.TaskID, "ou_m9", "同意"); err != nil {
		t.Fatal(err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("全员同意后实例 = %s, 期望 APPROVED", got)
	}
}

// TestAddSignNegatives 加签负向：非本人 → ErrNotAssignee；HELD 任务 → ErrTaskHeld。
func TestAddSignNegatives(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)
	a := taskFor(t, db, bizNo, "ou_a")
	if err := svc.AddSign(ctx, bizNo, a.TaskID, "ou_x", "ou_m9", "", ""); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非本人加签应 ErrNotAssignee，实际: %v", err)
	}
	b := taskFor(t, db, bizNo, "ou_b")
	if err := svc.AddSign(ctx, bizNo, b.TaskID, "ou_b", "ou_m9", "", ""); !errors.Is(err, flow.ErrTaskHeld) {
		t.Errorf("HELD 任务加签应 ErrTaskHeld，实际: %v", err)
	}
}

// ---------- 回退 ----------

// TestRollbackResetsEarlierNode 回退：被回退节点及其后节点整体 PENDING + 重置释放 + round+1。
func TestRollbackResetsEarlierNode(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_m2")

	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意"); err != nil {
		t.Fatal(err)
	}
	// 当前活动节点 = n2；回退到 n1。
	if err := svc.Rollback(ctx, bizNo, "ou_m1", "n1", "资料有误"); err != nil {
		t.Fatalf("回退失败: %v", err)
	}
	r1 := taskFor(t, db, bizNo, "ou_m1")
	if r1.Status != flow.TaskPending || r1.ReleaseState != flow.ReleaseReleased || r1.Round != 2 {
		t.Errorf("n1 重置 = {status:%s rel:%s round:%d}, 期望 {PENDING RELEASED 2}", r1.Status, r1.ReleaseState, r1.Round)
	}
	r2 := taskFor(t, db, bizNo, "ou_m2")
	if r2.Status != flow.TaskPending || r2.ReleaseState != flow.ReleaseHeld || r2.Round != 2 {
		t.Errorf("n2 重置 = {status:%s rel:%s round:%d}, 期望 {PENDING HELD 2}", r2.Status, r2.ReleaseState, r2.Round)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("回退后实例 = %s, 期望 PENDING", got)
	}
}

// TestRollbackNegatives 回退负向：回退到当前/更晚节点、非审批人、终态 → 必须被拒。
func TestRollbackNegatives(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_m2")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	_ = svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意") // 活动节点 = n2

	if err := svc.Rollback(ctx, bizNo, "ou_m1", "n2", ""); !errors.Is(err, flow.ErrIllegalTransition) {
		t.Errorf("回退到当前节点应 ErrIllegalTransition，实际: %v", err)
	}
	if err := svc.Rollback(ctx, bizNo, "ou_x", "n1", ""); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非审批人回退应 ErrNotAssignee，实际: %v", err)
	}
	_ = svc.Cancel(ctx, bizNo, "ou_app", "撤回")
	if err := svc.Rollback(ctx, bizNo, "ou_m1", "n1", ""); !errors.Is(err, flow.ErrIllegalTransition) {
		t.Errorf("终态回退应 ErrIllegalTransition，实际: %v", err)
	}
}

// ---------- 撤回 + 锁号 ----------

// TestCancelLocksNumber 撤回 → 终态；同号复用被拒；重新发起得新单号且 prev_biz_no 指向旧号。
func TestCancelLocksNumber(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitOneNode(t, svc, "ou_m1")

	if err := svc.Cancel(ctx, bizNo, "ou_app", "撤回"); err != nil {
		t.Fatal(err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceCanceled {
		t.Fatalf("撤回后实例 = %s, 期望 CANCELED", got)
	}
	// 同号复用（不同 instance_code、同 biz_no）必须被 UNIQUE(biz_no) 拒绝。
	err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "other-code", ApprovalCode: "code-pr", DocType: "PR", BizNo: bizNo,
		Status: flow.InstancePending, CreatedAt: flowAt, UpdatedAt: flowAt,
	})
	if err == nil {
		t.Errorf("同号复用应被 UNIQUE(biz_no) 拒绝，实际成功")
	}
	// 重新发起 → 新单号，prev_biz_no 指向旧号。
	newNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app", PrevBizNo: bizNo,
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_m1"}}}},
		At:    flowAt,
	})
	if err != nil {
		t.Fatalf("重发失败: %v", err)
	}
	if newNo == bizNo {
		t.Error("重发必须得到新单号")
	}
	if got := instOf(t, db, newNo).PrevBizNo; got != bizNo {
		t.Errorf("prev_biz_no = %s, 期望 %s", got, bizNo)
	}
}
