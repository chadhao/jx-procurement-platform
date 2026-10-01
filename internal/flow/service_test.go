package flow_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

var flowAt = time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)

// newFlowDB 建测试库并预置标准三方定义 `code-pr → PR`。
//
// ★ 为什么必须预置：Submit 现在会做 S7「提交前校验审批定义存在」（04a §10 / docs/11 R10）——
// 未注册 def 的 approval_code 会被可见地拒绝。用例要走到真实提交路径，就得先把定义备好（贴近生产）。
func newFlowDB(t *testing.T) *store.DB {
	t.Helper()
	db := storetest.NewDB(t)
	if err := db.UpsertApprovalDef(context.Background(), &store.ApprovalDef{
		ApprovalCode: "code-pr", DocType: "PR", Name: "采购申请", CallbackToken: "tok-pr",
	}); err != nil {
		t.Fatalf("预置审批定义失败: %v", err)
	}
	return db
}

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
	db := newFlowDB(t)
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
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNode(t, svc, db)

	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil); err != nil {
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
	if err := svc.Approve(ctx, bizNo, m2.TaskID, "ou_m2", "同意", nil); err != nil {
		t.Fatalf("同意 m2 失败: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Fatalf("node1 通过后（node2 未审）实例 = %s, 期望 PENDING", got)
	}

	// node2 同意 → 全部节点通过 → 实例 APPROVED。
	gm := taskFor(t, db, bizNo, "ou_gm")
	if err := svc.Approve(ctx, bizNo, gm.TaskID, "ou_gm", "同意", nil); err != nil {
		t.Fatalf("同意 gm 失败: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("全部节点通过后实例 = %s, 期望 APPROVED", got)
	}
}

// TestFutureNodeCannotApproveEarly ★ 不可能状态防御：未到达的节点不得提前审批。
func TestFutureNodeCannotApproveEarly(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNode(t, svc, db)

	gm := taskFor(t, db, bizNo, "ou_gm") // node2
	err := svc.Approve(ctx, bizNo, gm.TaskID, "ou_gm", "越级同意", nil)
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
	db := newFlowDB(t)
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
	db := newFlowDB(t)
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
	if err := svc.Approve(ctx, bizNo, task.TaskID, "ou_m1", "同意", nil); err != nil {
		t.Fatal(err)
	}
	before := instOf(t, db, bizNo)
	if before.Status != flow.InstanceApproved {
		t.Fatalf("实例状态 = %s, 期望 APPROVED", before.Status)
	}

	// 对终态实例再"同意"→ no-op（幂等），状态与版本号均不变。
	if err := svc.Approve(ctx, bizNo, task.TaskID, "ou_m1", "再来一次", nil); err != nil {
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
	db := newFlowDB(t)
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
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "撤回后同意", nil); err != nil {
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
	db := newFlowDB(t)
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
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "", nil); err != nil {
		t.Fatal(err)
	}
	check("同意 m1")

	m2 := taskFor(t, db, bizNo, "ou_m2")
	if err := svc.Approve(ctx, bizNo, m2.TaskID, "ou_m2", "", nil); err != nil {
		t.Fatal(err)
	}
	check("同意 m2")

	gm := taskFor(t, db, bizNo, "ou_gm")
	if err := svc.Approve(ctx, bizNo, gm.TaskID, "ou_gm", "", nil); err != nil {
		t.Fatal(err)
	}
	check("同意 gm（终审）")
}

// TestSubmitValidation 提交入参非法必须被拒（不静默建半成品）。
func TestSubmitValidation(t *testing.T) {
	db := newFlowDB(t)
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

// ---------- 顺序会签 · 分段释放（A6，04a §2.3） ----------

// submitSeqNode 提交一张「node1 会签 3 人 + node2 单人」的单据（顺序会签夹具）。
func submitSeqNode(t *testing.T, svc *flow.Service) string {
	t.Helper()
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app", ApplicantName: "张三",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "会签", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_a", Name: "甲"}, {OpenID: "ou_b", Name: "乙"}, {OpenID: "ou_c", Name: "丙"}}},
			{NodeID: "n2", NodeName: "终审", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_gm", Name: "总"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

// releaseOf 取某审批人任务的释放状态（HELD/RELEASED）。
func releaseOf(t *testing.T, db *store.DB, bizNo, assignee string) string {
	t.Helper()
	return taskFor(t, db, bizNo, assignee).ReleaseState
}

// TestSeqSignInitialOnlyFirstReleased ① 同 node 3 人：仅 1 RELEASED、2 HELD；
//
//	且后续 node 的任务在提交时全部 HELD（不得提前释放 → 飞书侧不生成待办）。
func TestSeqSignInitialOnlyFirstReleased(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	bizNo := submitSeqNode(t, svc)

	if got := releaseOf(t, db, bizNo, "ou_a"); got != flow.ReleaseReleased {
		t.Errorf("node1 首位 ou_a = %s, 期望 RELEASED", got)
	}
	for _, ap := range []string{"ou_b", "ou_c"} {
		if got := releaseOf(t, db, bizNo, ap); got != flow.ReleaseHeld {
			t.Errorf("node1 非首位 %s = %s, 期望 HELD", ap, got)
		}
	}
	if got := releaseOf(t, db, bizNo, "ou_gm"); got != flow.ReleaseHeld {
		t.Errorf("后续 node 的 ou_gm = %s, 期望 HELD（顺序会签不得提前释放）", got)
	}
}

// TestSeqSignReleasesNextOnApprove ② 第 1 人 APPROVED 后 → 第 2 人变 RELEASED、第 3 人仍 HELD。
func TestSeqSignReleasesNextOnApprove(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)

	a := taskFor(t, db, bizNo, "ou_a")
	if err := svc.Approve(ctx, bizNo, a.TaskID, "ou_a", "同意", nil); err != nil {
		t.Fatalf("同意 ou_a 失败: %v", err)
	}
	if got := releaseOf(t, db, bizNo, "ou_b"); got != flow.ReleaseReleased {
		t.Errorf("ou_b 在 ou_a 同意后 = %s, 期望 RELEASED", got)
	}
	if got := releaseOf(t, db, bizNo, "ou_c"); got != flow.ReleaseHeld {
		t.Errorf("ou_c 应仍 = HELD（尚未轮到），实际 %s", got)
	}
}

// TestSeqSignHeldTaskCannotApprove ③ 负向：未释放（HELD）任务上同意/拒绝**必须被拒**（非静默）。
func TestSeqSignHeldTaskCannotApprove(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)

	// ou_b 尚 HELD（ou_a 未同意）→ 同意必须返回 ErrTaskHeld。
	b := taskFor(t, db, bizNo, "ou_b")
	if err := svc.Approve(ctx, bizNo, b.TaskID, "ou_b", "抢跑", nil); !errors.Is(err, flow.ErrTaskHeld) {
		t.Fatalf("未释放任务同意应返回 ErrTaskHeld，实际: %v", err)
	}
	if err := svc.Reject(ctx, bizNo, b.TaskID, "ou_b", "抢跑拒绝"); !errors.Is(err, flow.ErrTaskHeld) {
		t.Fatalf("未释放任务拒绝应返回 ErrTaskHeld，实际: %v", err)
	}
	// 被拒后任务状态不得改变，实例仍 PENDING。
	if got := taskFor(t, db, bizNo, "ou_b"); got.Status != flow.TaskPending || got.ReleaseState != flow.ReleaseHeld {
		t.Errorf("非法操作改动了任务：status=%s release=%s", got.Status, got.ReleaseState)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("实例状态 = %s, 期望 PENDING", got)
	}
}

// TestSeqSignNextNodeOnlyAfterFullNode ④ 全 node APPROVED 才推进下一 node。
func TestSeqSignNextNodeOnlyAfterFullNode(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)

	approve := func(ap string) {
		t.Helper()
		tk := taskFor(t, db, bizNo, ap)
		if err := svc.Approve(ctx, bizNo, tk.TaskID, ap, "同意", nil); err != nil {
			t.Fatalf("同意 %s 失败: %v", ap, err)
		}
	}
	approve("ou_a")
	approve("ou_b")
	// node1 尚有 ou_c 未同意 → 下一 node 保持 HELD。
	if got := releaseOf(t, db, bizNo, "ou_gm"); got != flow.ReleaseHeld {
		t.Errorf("node1 未全员同意时 ou_gm = %s, 期望 HELD", got)
	}
	approve("ou_c") // node1 全员同意 → 释放下一 node
	if got := releaseOf(t, db, bizNo, "ou_gm"); got != flow.ReleaseReleased {
		t.Errorf("node1 全员同意后 ou_gm = %s, 期望 RELEASED", got)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("node2 未审时实例 = %s, 期望 PENDING", got)
	}
	approve("ou_gm")
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("全部通过后实例 = %s, 期望 APPROVED", got)
	}
}

// TestSeqSignRejectStopsRelease ⑤ 任一 REJECTED → 同 node 剩余任务不再释放。
func TestSeqSignRejectStopsRelease(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)

	a := taskFor(t, db, bizNo, "ou_a")
	if err := svc.Approve(ctx, bizNo, a.TaskID, "ou_a", "同意", nil); err != nil {
		t.Fatal(err)
	}
	// ou_a 同意后 ou_b 已释放；此时 ou_b 拒绝 → 实例驳回；ou_c 不得被释放。
	b := taskFor(t, db, bizNo, "ou_b")
	if err := svc.Reject(ctx, bizNo, b.TaskID, "ou_b", "驳回"); err != nil {
		t.Fatal(err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceRejected {
		t.Fatalf("驳回后实例 = %s, 期望 REJECTED", got)
	}
	if got := releaseOf(t, db, bizNo, "ou_c"); got != flow.ReleaseHeld {
		t.Errorf("驳回后 ou_c = %s, 期望 HELD（不再释放）", got)
	}
	if got := taskFor(t, db, bizNo, "ou_c").Status; got != flow.TaskDone {
		t.Errorf("驳回后 ou_c 任务 = %s, 期望 DONE（被动终结）", got)
	}
}

// TestSeqSignRepushDoesNotResetRelease ⑥ 快照重推（同 task_id 重写）不得把已 RELEASED/APPROVED 置回 HELD。
func TestSeqSignRepushDoesNotResetRelease(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)

	a := taskFor(t, db, bizNo, "ou_a")
	if err := svc.Approve(ctx, bizNo, a.TaskID, "ou_a", "同意", nil); err != nil {
		t.Fatal(err)
	}
	// 模拟「落后快照重推」：以同 task_id、旧状态（PENDING/HELD）再次 upsert。
	stale := []store.FlowTask{
		{TaskID: a.TaskID, BizNo: bizNo, NodeID: "n1", NodeSeq: 1, Round: 1,
			AssigneeOpenID: "ou_a", Status: flow.TaskPending, ReleaseState: flow.ReleaseHeld,
			CreatedAt: flowAt, UpdatedAt: flowAt},
		{TaskID: taskFor(t, db, bizNo, "ou_b").TaskID, BizNo: bizNo, NodeID: "n1", NodeSeq: 1, Round: 1,
			AssigneeOpenID: "ou_b", Status: flow.TaskPending, ReleaseState: flow.ReleaseHeld,
			CreatedAt: flowAt, UpdatedAt: flowAt},
	}
	for _, s := range stale {
		if err := db.WithTx(ctx, func(tx *sql.Tx) error { return db.UpsertFlowTaskTx(ctx, tx, &s) }); err != nil {
			t.Fatalf("重推 upsert 失败: %v", err)
		}
	}
	// ou_a：APPROVED + RELEASED 必须原封不动。
	if got := taskFor(t, db, bizNo, "ou_a"); got.Status != flow.TaskApproved || got.ReleaseState != flow.ReleaseReleased {
		t.Errorf("重推把已审批任务改回：status=%s release=%s（期望 APPROVED/RELEASED）", got.Status, got.ReleaseState)
	}
	// ou_b：已被释放，不得被旧快照置回 HELD。
	if got := releaseOf(t, db, bizNo, "ou_b"); got != flow.ReleaseReleased {
		t.Errorf("重推把已释放任务置回 %s（期望 RELEASED）", got)
	}
}

// TestListTasksByNodeOrdersByTaskOrder ★ 次序键必须是 `task_order`（显式契约），**不是** rowid/插入序。
//
// 故意让「插入顺序」与「task_order」不一致（插入序 = task_order 3,1,2）：
//
//	按 `rowid` / 插入序排序的实现会返回错误次序 → 本用例必红。
func TestListTasksByNodeOrdersByTaskOrder(t *testing.T) {
	db := newFlowDB(t)
	ctx := context.Background()
	biz := "PR-2609-9999"
	for _, spec := range []struct {
		suffix string
		order  int
	}{{"a", 3}, {"b", 1}, {"c", 2}} { // 插入顺序 ≠ task_order 顺序
		sp := spec
		if err := db.WithTx(ctx, func(tx *sql.Tx) error {
			return db.UpsertFlowTaskTx(ctx, tx, &store.FlowTask{
				TaskID: biz + "-n1-" + sp.suffix, BizNo: biz, NodeID: "n1", NodeSeq: 1, Round: 1,
				AssigneeOpenID: "ou_" + sp.suffix, Status: flow.TaskPending, TaskOrder: sp.order,
				CreatedAt: flowAt, UpdatedAt: flowAt,
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		ts, err := db.ListTasksByNodeTx(ctx, tx, biz, "n1")
		if err != nil {
			return err
		}
		for _, tk := range ts {
			got = append(got, tk.AssigneeOpenID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"ou_b", "ou_c", "ou_a"} // task_order 1,2,3
	if len(got) != len(want) {
		t.Fatalf("任务数 = %d, 期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("次序 = %v, 期望 %v（应按 task_order，非插入序/rowid）", got, want)
		}
	}
}

// TestTwoInstancesDistinctTaskIDs ★ 回归：task_id 必须含 biz_no。
//
// 否则第二个实例的同名任务（同 node_id/assignee/idx）会撞 `ON CONFLICT(task_id) DO NOTHING`
// 而**静默不建** —— 该实例审批链为空、永不推进（静默 P0）。
func TestTwoInstancesDistinctTaskIDs(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	sub := func() string {
		b, err := svc.Submit(ctx, flow.SubmitInput{
			DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
			Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
			At:    flowAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	b1, b2 := sub(), sub()
	if b1 == b2 {
		t.Fatalf("两次提交单号相同: %s", b1)
	}
	for _, b := range []string{b1, b2} {
		if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_flow_task WHERE biz_no = ?`, b); n != 1 {
			t.Errorf("实例 %s 任务数 = %d, 期望 1（task_id 撞车导致静默不建）", b, n)
		}
	}
}

// TestSeqSignOrderFollowsDeclarationNotLexicographic ★ 释放顺序必须跟随**声明序**（task_order），
// 而非 open_id 字典序 / task_id 次序。
//
// 构造：审批人的 open_id **字典序与声明序完全相反**（声明 [zz, mm, aa]；字典序 aa<mm<zz）。
// 若实现用 `task_id`（含 open_id）或 `rowid` 排序，则首个被释放者会是 `ou_aa` → 本用例必红。
func TestSeqSignOrderFollowsDeclarationNotLexicographic(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{
			{OpenID: "ou_zz"}, {OpenID: "ou_mm"}, {OpenID: "ou_aa"},
		}}},
		At: flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 声明序首个 = ou_zz 必须被释放；字典序最小的 ou_aa 必须 HELD。
	if got := releaseOf(t, db, bizNo, "ou_zz"); got != flow.ReleaseReleased {
		t.Fatalf("声明序首位 ou_zz = %s，期望 RELEASED（跟声明序，不跟字典序）", got)
	}
	for _, ap := range []string{"ou_mm", "ou_aa"} {
		if got := releaseOf(t, db, bizNo, ap); got != flow.ReleaseHeld {
			t.Errorf("%s = %s，期望 HELD", ap, got)
		}
	}

	// 逐个同意 → 释放顺序必须是 zz → mm → aa。
	approve := func(ap string) {
		t.Helper()
		tk := taskFor(t, db, bizNo, ap)
		if err := svc.Approve(ctx, bizNo, tk.TaskID, ap, "同意", nil); err != nil {
			t.Fatalf("同意 %s 失败: %v", ap, err)
		}
	}
	approve("ou_zz")
	if got := releaseOf(t, db, bizNo, "ou_mm"); got != flow.ReleaseReleased {
		t.Fatalf("ou_zz 同意后应释放 ou_mm，实际 %s", got)
	}
	if got := releaseOf(t, db, bizNo, "ou_aa"); got != flow.ReleaseHeld {
		t.Fatalf("ou_aa 此时应仍 HELD，实际 %s", got)
	}
	approve("ou_mm")
	if got := releaseOf(t, db, bizNo, "ou_aa"); got != flow.ReleaseReleased {
		t.Fatalf("ou_mm 同意后应释放 ou_aa，实际 %s", got)
	}
	approve("ou_aa")
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("全员同意后实例 = %s，期望 APPROVED", got)
	}
}

// TestSubmitRequiresRegisteredDefinition ① S7/R10：定义未注册 → 提交**可见地**被拒；已注册 → 通过。
func TestSubmitRequiresRegisteredDefinition(t *testing.T) {
	ctx := context.Background()

	// 未注册（裸库，不预置 def）→ 必须返回 ErrDefinitionMissing。
	rawDB := storetest.NewDB(t)
	rawSvc := flow.New(rawDB, "app")
	_, err := rawSvc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-not-registered", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:    flowAt,
	})
	if !errors.Is(err, flow.ErrDefinitionMissing) {
		t.Fatalf("定义未注册应返回 ErrDefinitionMissing，实际: %v", err)
	}
	// 可见失败：不产生任何半成品实例（校验在事务之前）。
	if n := storetest.Count(t, rawDB, `SELECT COUNT(*) FROM t_instance`); n != 0 {
		t.Errorf("定义缺失时不应落实例，实际 %d 条", n)
	}

	// 已注册 → 通过。
	okDB := newFlowDB(t)
	okSvc := flow.New(okDB, "app")
	if _, err := okSvc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:    flowAt,
	}); err != nil {
		t.Fatalf("定义已注册应可提交，实际: %v", err)
	}
}

// ---------- 两键准入（operator == assignee，定案 #60） ----------

// TestApproveRequiresAssignee ★ 负向（路径一：`act` 直调）：**非本人** `approve`/`reject` **必须被拒**。
//
// ★ 口径＝用户第二轮口径 ⑥：代理人只能"转交 / 退回"，**不能代替同意** → 同意/拒绝须本人。
func TestApproveRequiresAssignee(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_m2")
	m1 := taskFor(t, db, bizNo, "ou_m1")

	// 非本人（ou_evil）approve / reject → 均须 ErrNotAssignee。
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_evil", "替签", nil); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非本人 approve 应 ErrNotAssignee，实际: %v", err)
	}
	if err := svc.Reject(ctx, bizNo, m1.TaskID, "ou_evil", "替拒"); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非本人 reject 应 ErrNotAssignee，实际: %v", err)
	}
	// 可见失败：任务/实例均未被改动（不得被"代签"推进）。
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskPending {
		t.Errorf("非本人操作改动了任务：status=%s，期望 PENDING", got)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("非本人操作推进了实例：%s，期望 PENDING", got)
	}
	// 对照：本人 approve → 成功推进。
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil); err != nil {
		t.Fatalf("本人 approve 应成功，实际: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskApproved {
		t.Errorf("本人 approve 后任务 = %s，期望 APPROVED", got)
	}
}

// TestCallbackRejectsNonAssignee ★ 负向（路径二：经 `HandleCallback`）：回调里的
// `operator` ≠ `assignee` → **必须被拒**，且**不得推进**实例。
//
// ★ 该用例同时证明「operator 传参链路」正确：回调用例的 advancer 与生产一致
//
//	（`bootstrap.go:175` 同步适配器），把 `req.OperatorOpenID` 透传给 `flow.Approve` → `act`。
func TestCallbackRejectsNonAssignee(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	// advancer 与生产装配一致：把回调里的**真实 operator** 透传给 act（不自造、不传空）。
	svc.SetCallbackAdvancer(func(ctx context.Context, req flow.CallbackRequest) error {
		if req.OpType == flow.OpReject {
			return svc.Reject(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason)
		}
		return svc.Approve(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason, nil)
	})

	// operator = ou_evil ≠ assignee(ou_m1) → 必须被拒（推进不得发生）。
	_, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: flow.OpApprove, OperatorOpenID: "ou_evil",
	})
	if !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非本人回调应 ErrNotAssignee，实际: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("非本人回调后实例 = %s，期望 PENDING（不得推进）", got)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskPending {
		t.Errorf("非本人回调后任务 = %s，期望 PENDING", got)
	}

	// 对照：本人回调 → 推进。另起实例仅因本实例的 assignee 是 ou_m1（而非 ou_m2）。
	// ★ 注：准入失败已不再占键（定案 #62）——「被拒后同键真实回调仍能推进」的**完整因果链**专测见
	//   callback_test.go::TestCallbackAdmissionDoesNotConsumeIdempotencyKey。
	bizNo2 := submitOneNode(t, svc, "ou_m2")
	m2 := taskFor(t, db, bizNo2, "ou_m2")
	if _, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo2, TaskID: m2.TaskID, OpType: flow.OpApprove, OperatorOpenID: "ou_m2",
	}); err != nil {
		t.Fatalf("本人回调应成功，实际: %v", err)
	}
	if got := instOf(t, db, bizNo2).Status; got != flow.InstanceApproved {
		t.Errorf("本人回调后实例 = %s，期望 APPROVED", got)
	}
}
