package flow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

var flowAt = time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)

// submitTwoNode 提交一张两节点（node1 会签 2 人、node2 单人）的单据。
func submitTwoNode(t *testing.T, svc *flow.Service, db *store.DB) string {
	t.Helper()
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "主管", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_m1", Name: "李四"}, {OpenID: "ou_m2", Name: "赵六"}}},
			{NodeID: "n2", NodeName: "总经理", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_gm", Name: "王五"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

// taskFor 按审批人取某实例的任务。
func taskFor(t *testing.T, db *store.DB, bizNo, assignee string) store.FlowTask {
	t.Helper()
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	for _, tk := range tasks {
		if tk.AssigneeOpenID == assignee {
			return tk
		}
	}
	t.Fatalf("未找到 assignee=%s 的任务（biz_no=%s）", assignee, bizNo)
	return store.FlowTask{}
}

func instOf(t *testing.T, db *store.DB, bizNo string) store.Instance {
	t.Helper()
	inst, err := db.GetInstanceByBizNo(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读取实例失败: %v", err)
	}
	return *inst
}

// TestSubmitCreatesInstanceAndChain 提交：单号 + 实例（PENDING, update_time=1）+ 全链任务。
func TestSubmitCreatesInstanceAndChain(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo := submitTwoNode(t, svc, db)

	if bizNo != "PR-2609-0001" {
		t.Errorf("单号 = %s, 期望 PR-2609-0001", bizNo)
	}
	inst := instOf(t, db, bizNo)
	if inst.Status != flow.InstancePending {
		t.Errorf("实例状态 = %s, 期望 PENDING", inst.Status)
	}
	if inst.UpdateTime != 1 {
		t.Errorf("update_time = %d, 期望 1", inst.UpdateTime)
	}
	if inst.InstanceCode != "app:"+bizNo {
		t.Errorf("instance_code = %s, 期望 app:%s（04a §3.3 应用前缀）", inst.InstanceCode, bizNo)
	}
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Errorf("任务数 = %d, 期望 3（会签 2 + 末节点 1）", len(tasks))
	}
}

// TestCoSignNodeRequiresAllApproved ★ 会签：节点未全员同意，不得推进。
func TestCoSignNodeRequiresAllApproved(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNode(t, svc, db)

	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意"); err != nil {
		t.Fatalf("同意 m1 失败: %v", err)
	}
	// 会签下仅 1/2 同意：实例仍 PENDING，node1 仍有待审任务。
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Fatalf("会签未全员同意却推进：实例状态 = %s, 期望 PENDING", got)
	}
	m2 := taskFor(t, db, bizNo, "ou_m2")
	if m2.Status != flow.TaskPending {
		t.Errorf("node1 未全员同意，m2 任务状态 = %s, 期望 PENDING", m2.Status)
	}

	// m2 同意 → node1 通过；实例仍 PENDING（等待 node2）。
	if err := svc.Approve(ctx, bizNo, m2.TaskID, "ou_m2", "同意"); err != nil {
		t.Fatalf("同意 m2 失败: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Fatalf("node1 通过后（node2 未审）实例 = %s, 期望 PENDING", got)
	}

	// node2 同意 → 全部节点通过 → 实例 APPROVED。
	gm := taskFor(t, db, bizNo, "ou_gm")
	if err := svc.Approve(ctx, bizNo, gm.TaskID, "ou_gm", "同意"); err != nil {
		t.Fatalf("同意 gm 失败: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("全部节点通过后实例 = %s, 期望 APPROVED", got)
	}
}

// TestFutureNodeCannotApproveEarly ★ 不可能状态防御：未到达的节点不得提前审批。
func TestFutureNodeCannotApproveEarly(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNode(t, svc, db)

	gm := taskFor(t, db, bizNo, "ou_gm") // node2
	err := svc.Approve(ctx, bizNo, gm.TaskID, "ou_gm", "越级同意")
	if !errors.Is(err, flow.ErrNodeNotReached) {
		t.Fatalf("提前审批 node2 应返回 ErrNodeNotReached，实际: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("非法操作后实例状态 = %s, 期望 PENDING（不得被推进）", got)
	}
	// node2 任务仍 PENDING（未被改动）。
	if got := taskFor(t, db, bizNo, "ou_gm").Status; got != flow.TaskPending {
		t.Errorf("node2 任务状态 = %s, 期望 PENDING", got)
	}
}

// TestRejectTerminalizesInstance 任一节点驳回 → 实例 REJECTED，其余在途任务 DONE。
func TestRejectTerminalizesInstance(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNode(t, svc, db)

	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Reject(ctx, bizNo, m1.TaskID, "ou_m1", "预算不符"); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceRejected {
		t.Fatalf("驳回后实例 = %s, 期望 REJECTED", got)
	}
	for _, ap := range []string{"ou_m2", "ou_gm"} {
		if got := taskFor(t, db, bizNo, ap).Status; got != flow.TaskDone {
			t.Errorf("%s 任务状态 = %s, 期望 DONE（被动终结）", ap, got)
		}
	}
}

// TestTerminalInstanceNoRegression ★ 终态不得回退：APPROVED 后同意/拒绝均为幂等 no-op。
func TestTerminalInstanceNoRegression(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_m1"}}}},
		At:    flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, task.TaskID, "ou_m1", "同意"); err != nil {
		t.Fatal(err)
	}
	before := instOf(t, db, bizNo)
	if before.Status != flow.InstanceApproved {
		t.Fatalf("实例状态 = %s, 期望 APPROVED", before.Status)
	}

	// 对终态实例再"同意"→ no-op（幂等），状态与版本号均不变。
	if err := svc.Approve(ctx, bizNo, task.TaskID, "ou_m1", "再来一次"); err != nil {
		t.Fatalf("终态重复同意应为 no-op，实际报错: %v", err)
	}
	// 试图"拒绝"终态实例 → 亦为 no-op，绝不回退。
	if err := svc.Reject(ctx, bizNo, task.TaskID, "ou_m1", "试图回退"); err != nil {
		t.Fatalf("终态拒绝应为 no-op，实际报错: %v", err)
	}
	after := instOf(t, db, bizNo)
	if after.Status != flow.InstanceApproved {
		t.Errorf("终态被回退：%s → %s", before.Status, after.Status)
	}
	if after.UpdateTime != before.UpdateTime {
		t.Errorf("终态 no-op 不应改变 update_time：%d → %d", before.UpdateTime, after.UpdateTime)
	}
}

// TestCanceledCannotAdvance ★ CANCELED 后不得推进。
func TestCanceledCannotAdvance(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNode(t, svc, db)

	if err := svc.Cancel(ctx, bizNo, "ou_app", "撤回"); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	inst := instOf(t, db, bizNo)
	if inst.Status != flow.InstanceCanceled {
		t.Fatalf("撤回后实例 = %s, 期望 CANCELED", inst.Status)
	}
	if inst.CancelReason != "撤回" || inst.CancelAt == nil {
		t.Errorf("撤回原因/时刻未记录: reason=%q at=%v", inst.CancelReason, inst.CancelAt)
	}
	// 全部在途任务被动终结。
	for _, ap := range []string{"ou_m1", "ou_m2", "ou_gm"} {
		if got := taskFor(t, db, bizNo, ap).Status; got != flow.TaskDone {
			t.Errorf("%s 任务状态 = %s, 期望 DONE", ap, got)
		}
	}
	// CANCELED 后同意/拒绝均 no-op，状态不得改变。
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "撤回后同意"); err != nil {
		t.Fatalf("CANCELED 后同意应 no-op: %v", err)
	}
	if err := svc.Reject(ctx, bizNo, m1.TaskID, "ou_m1", "撤回后拒绝"); err != nil {
		t.Fatalf("CANCELED 后拒绝应 no-op: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceCanceled {
		t.Errorf("CANCELED 后被推进：%s", got)
	}
	// 重复撤回幂等。
	if err := svc.Cancel(ctx, bizNo, "ou_app", "再撤一次"); err != nil {
		t.Errorf("重复撤回应幂等无错: %v", err)
	}
}

// TestUpdateTimeStrictlyIncreases update_time 在每次状态变更后严格递增（04a §3.1）。
func TestUpdateTimeStrictlyIncreases(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNode(t, svc, db)

	last := instOf(t, db, bizNo).UpdateTime // =1
	check := func(step string) {
		t.Helper()
		cur := instOf(t, db, bizNo).UpdateTime
		if cur <= last {
			t.Errorf("%s 后 update_time 未递增：%d → %d", step, last, cur)
		}
		last = cur
	}

	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", ""); err != nil {
		t.Fatal(err)
	}
	check("同意 m1")

	m2 := taskFor(t, db, bizNo, "ou_m2")
	if err := svc.Approve(ctx, bizNo, m2.TaskID, "ou_m2", ""); err != nil {
		t.Fatal(err)
	}
	check("同意 m2")

	gm := taskFor(t, db, bizNo, "ou_gm")
	if err := svc.Approve(ctx, bizNo, gm.TaskID, "ou_gm", ""); err != nil {
		t.Fatal(err)
	}
	check("同意 gm（终审）")
}

// TestSubmitValidation 提交入参非法必须被拒（不静默建半成品）。
func TestSubmitValidation(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	if _, err := svc.Submit(ctx, flow.SubmitInput{DocType: "PR"}); !errors.Is(err, flow.ErrInvalidSubmit) {
		t.Errorf("空审批链应返回 ErrInvalidSubmit，实际: %v", err)
	}
	if _, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR",
		Nodes:   []flow.NodeSpec{{NodeID: "n1", Seq: 1}}, // 无审批人
	}); !errors.Is(err, flow.ErrInvalidSubmit) {
		t.Errorf("无审批人节点应返回 ErrInvalidSubmit，实际: %v", err)
	}
	// 不得落任何实例。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_instance`); n != 0 {
		t.Errorf("非法提交不应落实例，实际 %d 条", n)
	}
}
