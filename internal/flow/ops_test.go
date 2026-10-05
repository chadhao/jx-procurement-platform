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
	if err := svc.Approve(ctx, bizNo, m9.TaskID, "ou_m9", "同意", nil); err != nil {
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
	if err := svc.AddSign(ctx, bizNo, m1.TaskID, "ou_m1", "ou_m9", "加签人", "补充意见", flow.AddSignAfter); err != nil {
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
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil); err != nil {
		t.Fatal(err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Fatalf("加签未审时实例 = %s, 期望 PENDING（原审批人不得独过）", got)
	}
	if got := taskFor(t, db, bizNo, "ou_m9").ReleaseState; got != flow.ReleaseReleased {
		t.Errorf("加签任务应被释放，实际 %s", got)
	}
	// 加签人同意 → 节点通过 → APPROVED。
	if err := svc.Approve(ctx, bizNo, m9.TaskID, "ou_m9", "同意", nil); err != nil {
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
	if err := svc.AddSign(ctx, bizNo, a.TaskID, "ou_x", "ou_m9", "", "", flow.AddSignAfter); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非本人加签应 ErrNotAssignee，实际: %v", err)
	}
	b := taskFor(t, db, bizNo, "ou_b")
	if err := svc.AddSign(ctx, bizNo, b.TaskID, "ou_b", "ou_m9", "", "", flow.AddSignAfter); !errors.Is(err, flow.ErrTaskHeld) {
		t.Errorf("HELD 任务加签应 ErrTaskHeld，实际: %v", err)
	}
}

// ---------- 加签时机 · 前置 / 后置（G2，01a §4.3） ----------

// releasedPendingCount 统计整实例「可办理」(RELEASED ∧ PENDING) 任务数。
//
// ★ 顺序会签不变量（04a §2.3）：任一时刻**恰 1 个**（终态除外）。
//   - 加签前置把可办理者由「当前办理人」**转为「新增者」**（不是两个都能办）；
//   - 已通过者虽仍 `RELEASED`，但 `status=APPROVED` → **不计入**（不变量针对「可办理」）。
func releasedPendingCount(t *testing.T, db *store.DB, bizNo string) int {
	t.Helper()
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	n := 0
	for _, tk := range tasks {
		if tk.ReleaseState == flow.ReleaseReleased && tk.Status == flow.TaskPending {
			n++
		}
	}
	return n
}

// nodeOrderSeq 返回某节点各任务 `task_order`（`ListFlowTasks` 已按 `node_seq, task_order` 升序）。
func nodeOrderSeq(t *testing.T, db *store.DB, bizNo, nodeID string) []int {
	t.Helper()
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	var out []int
	for _, tk := range tasks {
		if tk.NodeID == nodeID {
			out = append(out, tk.TaskOrder)
		}
	}
	return out
}

// TestAddSignBeforeInsertsBeforeCurrent 前置加签（正向）：新增者排到「当前办理人」**之前** →
//
//	先加者先审；当前办理人让位（RELEASED→HELD）但**仍在链上**；不变量恒 = 1。
func TestAddSignBeforeInsertsBeforeCurrent(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc) // n1: ou_a(1) RELEASED；ou_b/c HELD；n2: ou_gm HELD

	if got := releasedPendingCount(t, db, bizNo); got != 1 {
		t.Fatalf("加签前 RELEASED∧PENDING = %d，期望 1", got)
	}
	a := taskFor(t, db, bizNo, "ou_a") // 当前办理人

	// ou_a 前置加签 ou_z：新加者先审。
	if err := svc.AddSign(ctx, bizNo, a.TaskID, "ou_a", "ou_z", "前置人", "先审", flow.AddSignBefore); err != nil {
		t.Fatalf("前置加签失败: %v", err)
	}
	z := taskFor(t, db, bizNo, "ou_z")
	if z.NodeID != "n1" {
		t.Errorf("前置任务 node_id = %s，期望 n1（并入同 node）", z.NodeID)
	}
	if z.Status != flow.TaskPending || z.ReleaseState != flow.ReleaseReleased {
		t.Errorf("前置任务 = {status:%s release:%s}，期望 {PENDING RELEASED}（先加者先审）",
			z.Status, z.ReleaseState)
	}
	aAfter := taskFor(t, db, bizNo, "ou_a")
	if z.TaskOrder >= aAfter.TaskOrder {
		t.Errorf("前置 task_order=%d 未排到当前办理人（%d）之前", z.TaskOrder, aAfter.TaskOrder)
	}
	// 当前办理人让位：RELEASED → HELD（仍在链上、仍 PENDING）。
	if aAfter.Status != flow.TaskPending || aAfter.ReleaseState != flow.ReleaseHeld {
		t.Errorf("让位后当前办理人 = {status:%s release:%s}，期望 {PENDING HELD}",
			aAfter.Status, aAfter.ReleaseState)
	}
	// 不变量：整实例仍恰 1 个可办理（＝新增者）。
	if got := releasedPendingCount(t, db, bizNo); got != 1 {
		t.Errorf("前置加签后 RELEASED∧PENDING = %d，期望 1（不得两人同时可办）", got)
	}

	// 新增者先审 → 通过后当前办理人才被释放（顺序链前进一步）。
	if err := svc.Approve(ctx, bizNo, z.TaskID, "ou_z", "同意", nil); err != nil {
		t.Fatalf("前置人同意失败: %v", err)
	}
	if got := releasedPendingCount(t, db, bizNo); got != 1 {
		t.Errorf("前置人同意后 RELEASED∧PENDING = %d，期望 1", got)
	}
	if got := taskFor(t, db, bizNo, "ou_a").ReleaseState; got != flow.ReleaseReleased {
		t.Errorf("前置人同意后当前办理人应被释放，实际 %s", got)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("节点未全部通过时实例 = %s，期望 PENDING", got)
	}
	// 当前办理人同意 → 继续按链推进（ou_b 释放）。
	if err := svc.Approve(ctx, bizNo, aAfter.TaskID, "ou_a", "同意", nil); err != nil {
		t.Fatalf("当前办理人同意失败: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_b").ReleaseState; got != flow.ReleaseReleased {
		t.Errorf("ou_a 同意后 ou_b 应被释放，实际 %s", got)
	}
}

// TestAddSignAfterDefaultAndExplicit 后置（回归 + 缺省）：空 timing 与 AddSignAfter 同义 → 追加到队尾。
//
// ★ 回归保护：G2 引入 `timing` 入参后，**既有调用方语义（缺省后置）不得改变**。
func TestAddSignAfterDefaultAndExplicit(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc) // n1: ou_a(1) RELEASED；ou_b(2)/ou_c(3) HELD

	a := taskFor(t, db, bizNo, "ou_a")
	// 缺省（空串）→ 后置；必须与显式 AddSignAfter 同义。
	if err := svc.AddSign(ctx, bizNo, a.TaskID, "ou_a", "ou_z", "后置人", "", ""); err != nil {
		t.Fatalf("缺省加签失败: %v", err)
	}
	z := taskFor(t, db, bizNo, "ou_z")
	b := taskFor(t, db, bizNo, "ou_b")
	c := taskFor(t, db, bizNo, "ou_c")
	if !(z.TaskOrder > b.TaskOrder && z.TaskOrder > c.TaskOrder) {
		t.Errorf("缺省（后置）task_order=%d 未追加到队尾（ou_b=%d, ou_c=%d）", z.TaskOrder, b.TaskOrder, c.TaskOrder)
	}
	if z.ReleaseState != flow.ReleaseHeld {
		t.Errorf("后置新任务应先 HELD，实际 %s", z.ReleaseState)
	}
	// 后置不改变当前办理人的可办理态（仍是 ou_a）。
	if got := taskFor(t, db, bizNo, "ou_a").ReleaseState; got != flow.ReleaseReleased {
		t.Errorf("后置加签不应改动当前办理人释放态，实际 %s", got)
	}
	if got := releasedPendingCount(t, db, bizNo); got != 1 {
		t.Errorf("后置加签后 RELEASED∧PENDING = %d，期望 1", got)
	}
}

// TestAddSignBeforeDoesNotReReviewApproved ★ 负向①：前置**不得重审、不得重排已 `APPROVED` 者**。
func TestAddSignBeforeDoesNotReReviewApproved(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)

	a := taskFor(t, db, bizNo, "ou_a")
	if err := svc.Approve(ctx, bizNo, a.TaskID, "ou_a", "同意", nil); err != nil {
		t.Fatalf("ou_a 同意失败: %v", err)
	}
	aBefore := taskFor(t, db, bizNo, "ou_a")
	if aBefore.Status != flow.TaskApproved {
		t.Fatalf("ou_a 应为 APPROVED，实际 %s", aBefore.Status)
	}
	// 当前办理人 = ou_b；ou_b 前置加签 ou_z。
	b := taskFor(t, db, bizNo, "ou_b")
	if err := svc.AddSign(ctx, bizNo, b.TaskID, "ou_b", "ou_z", "前置人", "", flow.AddSignBefore); err != nil {
		t.Fatalf("前置加签失败: %v", err)
	}
	// 已通过者：状态与次序均**不得**被改动。
	aAfter := taskFor(t, db, bizNo, "ou_a")
	if aAfter.Status != flow.TaskApproved {
		t.Errorf("已通过者被重审：status %s → %s", aBefore.Status, aAfter.Status)
	}
	if aAfter.TaskOrder != aBefore.TaskOrder {
		t.Errorf("已通过者 task_order 被改动：%d → %d（前置插到当前办理人之前，不得重排已通过者）",
			aBefore.TaskOrder, aAfter.TaskOrder)
	}
	// 次序：已通过者(ou_a) < 新增者(ou_z) < 当前办理人(ou_b)。
	z := taskFor(t, db, bizNo, "ou_z")
	bAfter := taskFor(t, db, bizNo, "ou_b")
	if !(aAfter.TaskOrder < z.TaskOrder && z.TaskOrder < bAfter.TaskOrder) {
		t.Errorf("次序错误：需 ou_a(%d) < ou_z(%d) < ou_b(%d)",
			aAfter.TaskOrder, z.TaskOrder, bAfter.TaskOrder)
	}
}

// TestAddSignTerminalRejected ★ 负向②：实例终态 → 加签（前置/后置）**一律** `ErrIllegalTransition`。
func TestAddSignTerminalRejected(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)
	a := taskFor(t, db, bizNo, "ou_a")
	if err := svc.Cancel(ctx, bizNo, "ou_app", "撤回"); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	for _, timing := range []flow.AddSignTiming{flow.AddSignAfter, flow.AddSignBefore} {
		if err := svc.AddSign(ctx, bizNo, a.TaskID, "ou_a", "ou_z", "", "", timing); !errors.Is(err, flow.ErrIllegalTransition) {
			t.Errorf("终态实例加签(%s)应 ErrIllegalTransition，实际: %v", timing, err)
		}
	}
	// 可见失败：不产生半成品任务。
	tasks, err := db.ListFlowTasks(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		if tk.AssigneeOpenID == "ou_z" {
			t.Errorf("终态加签不应落任务，实际新增 assignee=ou_z")
		}
	}
}

// TestAddSignBeforeTaskOrderUniqueContiguous ★ 负向③：前置加签后同节点 `task_order` **唯一且连续**（无重复/无空洞）。
//
// 前置依赖 `order ≥ 当前办理人 order` 的整体 +1 腾位；若漏平移或平移阈值错 → 出现重复或空洞 → 本用例必红。
func TestAddSignBeforeTaskOrderUniqueContiguous(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)
	a := taskFor(t, db, bizNo, "ou_a")
	if err := svc.AddSign(ctx, bizNo, a.TaskID, "ou_a", "ou_z", "", "", flow.AddSignBefore); err != nil {
		t.Fatalf("前置加签失败: %v", err)
	}
	orders := nodeOrderSeq(t, db, bizNo, "n1")
	if len(orders) != 4 {
		t.Fatalf("前置加签后同节点任务数 = %d，期望 4（3 原 + 1 新）", len(orders))
	}
	seen := map[int]bool{}
	for i, o := range orders {
		if seen[o] {
			t.Fatalf("同节点 task_order 重复：%v（前置必须整体 +1 腾位，不得撞位）", orders)
		}
		seen[o] = true
		if o != i+1 {
			t.Fatalf("同节点 task_order 非连续：%v（期望 1..%d）", orders, len(orders))
		}
	}
}

// TestAddSignInvalidTimingRejected 负向④：非法 `timing` 必须**可见地失败**（`ErrInvalidSubmit`），不得静默按后置处理。
func TestAddSignInvalidTimingRejected(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc)
	a := taskFor(t, db, bizNo, "ou_a")
	err := svc.AddSign(ctx, bizNo, a.TaskID, "ou_a", "ou_z", "", "", flow.AddSignTiming("SIDEWAYS"))
	if !errors.Is(err, flow.ErrInvalidSubmit) {
		t.Errorf("非法加签时机应 ErrInvalidSubmit，实际: %v", err)
	}
	tasks, lerr := db.ListFlowTasks(ctx, bizNo)
	if lerr != nil {
		t.Fatal(lerr)
	}
	for _, tk := range tasks {
		if tk.AssigneeOpenID == "ou_z" {
			t.Errorf("非法加签时机不应落任务，实际新增 assignee=ou_z")
		}
	}
}

// TestAddSignApprovedTaskRejected ★ 负向⑤（定案 #57）：已 `APPROVED` 但**仍 `RELEASED`** 的任务上
// 加签**必须被拒**。
//
// ★ 为什么这条能戳中「静默缺口」：顺序会签下释放态**单向**、审批通过**不清释放态** →
// 已通过者的任务 **`RELEASED≠HELD` 恒成立**；若 `AddSign` 只看释放态（不看 `status`），
// **两个成立的不变量（① RELEASED∧PENDING=1；② 不重审已通过者）都看不出**它放行了
// 「对已通过任务再次加签」。这正是 README 定案 #57：「不变量成立」≠「语义正确」。
func TestAddSignApprovedTaskRejected(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSeqNode(t, svc) // n1: ou_a(1) RELEASED；ou_b/c HELD；n2: ou_gm HELD

	a := taskFor(t, db, bizNo, "ou_a")
	if err := svc.Approve(ctx, bizNo, a.TaskID, "ou_a", "同意", nil); err != nil {
		t.Fatalf("ou_a 同意失败: %v", err)
	}
	// 夹具自检：ou_a 已 APPROVED，但其任务**仍 RELEASED**（顺序会签不清理释放态）。
	aAfter := taskFor(t, db, bizNo, "ou_a")
	if aAfter.Status != flow.TaskApproved || aAfter.ReleaseState != flow.ReleaseReleased {
		t.Fatalf("夹具不符：ou_a = {status:%s release:%s}，期望 {APPROVED RELEASED}",
			aAfter.Status, aAfter.ReleaseState)
	}

	// 已 APPROVED 的任务，加签（前置/后置）一律可见拒绝。
	for _, timing := range []flow.AddSignTiming{flow.AddSignAfter, flow.AddSignBefore} {
		err := svc.AddSign(ctx, bizNo, aAfter.TaskID, "ou_a", "ou_z", "", "", timing)
		if !errors.Is(err, flow.ErrIllegalTransition) {
			t.Errorf("已 APPROVED 任务加签(%s)应 ErrIllegalTransition，实际: %v", timing, err)
		}
	}
	// 可见失败：不落新任务、不改变链上任务数。
	tasks, err := db.ListFlowTasks(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		if tk.AssigneeOpenID == "ou_z" {
			t.Errorf("已 APPROVED 任务加签不应落任务，实际新增 assignee=ou_z")
		}
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
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil); err != nil {
		t.Fatal(err)
	}
	// 当前活动节点 = n2（当前办理人 = ou_m2）；★ 回退准入收紧后，须由**当前办理人**发起。
	if err := svc.Rollback(ctx, bizNo, "ou_m2", "n1", "资料有误"); err != nil {
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
	_ = svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil) // 活动节点 = n2（当前办理人 = ou_m2）

	// ★ 由**当前办理人**（ou_m2）回退到当前/更晚节点 → 非法（须回退到**更早**节点）。
	if err := svc.Rollback(ctx, bizNo, "ou_m2", "n2", ""); !errors.Is(err, flow.ErrIllegalTransition) {
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

// TestRollbackRequiresCurrentAssignee ★ 负向（定案 #58）：**上游节点已通过者**发起回退 → **必须被拒**。
//
// ★ 为什么能戳中：旧判据 `actorHasTask`＝「实例内**任一**任务持有者」。已通过者（ou_m1）**仍持有任务**
// （其 `status=APPROVED`、`release_state` 仍 `RELEASED`）→ 旧判据**放行**；收紧后须为
// **当前活动节点（n2）的当前 `PENDING` 任务** assignee（ou_m2）才行。
func TestRollbackRequiresCurrentAssignee(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_m2")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil); err != nil {
		t.Fatal(err)
	} // 活动节点 = n2，当前办理人 = ou_m2

	// 上游已通过者（ou_m1）发起回退 → ErrNotAssignee。
	if err := svc.Rollback(ctx, bizNo, "ou_m1", "n1", "越权回退"); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("上游已通过者回退应 ErrNotAssignee，实际: %v", err)
	}
	// 可见失败：实例仍 PENDING、n1 任务**未被重置**（round 仍 1、状态仍 APPROVED）。
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("越权回退后实例 = %s，期望 PENDING（未被改动）", got)
	}
	r1 := taskFor(t, db, bizNo, "ou_m1")
	if r1.Status != flow.TaskApproved || r1.Round != 1 {
		t.Errorf("越权回退改动了 n1：status=%s round=%d，期望 APPROVED/round=1", r1.Status, r1.Round)
	}
	// 当前办理人（ou_m2）回退 → 合法，n1 被重置（round=2）。
	if err := svc.Rollback(ctx, bizNo, "ou_m2", "n1", "资料有误"); err != nil {
		t.Fatalf("当前办理人回退应成功，实际: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Round; got != 2 {
		t.Errorf("合法回退后 n1 round = %d，期望 2（被重置）", got)
	}
}

// TestCancelRequiresApplicant ★ 负向（定案 #58）：**非申请人**撤回他人单据 → **必须被拒**；且
// 鉴权**先于**幂等分支（非申请人对已撤回单亦应被拒 → 不泄漏"该单状态"）。
func TestCancelRequiresApplicant(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_m2")

	// 非申请人（ou_m1，即使他是审批人）撤回 → ErrNotAssignee。
	if err := svc.Cancel(ctx, bizNo, "ou_m1", "越权撤回"); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非申请人撤回应 ErrNotAssignee，实际: %v", err)
	}
	// 可见失败：实例未被撤回。
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("越权撤回后实例 = %s，期望 PENDING（未被改动）", got)
	}
	// 申请人本人撤回 → 成功。
	if err := svc.Cancel(ctx, bizNo, "ou_app", "撤回"); err != nil {
		t.Fatalf("申请人撤回应成功，实际: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceCanceled {
		t.Fatalf("撤回后实例 = %s，期望 CANCELED", got)
	}
	// ★ 信息泄漏防护：非申请人对**已 CANCELED** 的单再撤回 → 仍 ErrNotAssignee（鉴权先于幂等 no-op）。
	if err := svc.Cancel(ctx, bizNo, "ou_m1", "探测状态"); !errors.Is(err, flow.ErrNotAssignee) {
		t.Errorf("非申请人对已撤回单撤回应 ErrNotAssignee（鉴权须先于幂等），实际: %v", err)
	}
	// 申请人重复撤回 → 幂等 no-op。
	if err := svc.Cancel(ctx, bizNo, "ou_app", "再撤一次"); err != nil {
		t.Errorf("申请人重复撤回应幂等无错，实际: %v", err)
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

// ---------- N-060 F4：四操作次数上限 3/3/2（FR-M9-04/05/06） ----------

// pendingTaskFor 该 assignee 的 **PENDING** 任务（★ 通用 taskFor 返回首个匹配 ——
// 含转交后残留的 TRANSFERRED 旧任务 ⇒ 上限循环须按 PENDING 取当前任务；不动 taskFor 本体）。
func pendingTaskFor(t *testing.T, db *store.DB, bizNo, assignee string) store.FlowTask {
	t.Helper()
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	for _, tk := range tasks {
		if tk.AssigneeOpenID == assignee && tk.Status == flow.TaskPending {
			return tk
		}
	}
	t.Fatalf("未找到 assignee=%s 的 PENDING 任务（biz_no=%s）", assignee, bizNo)
	return store.FlowTask{}
}

// TestOpLimitsTransferAddSignRollback 上限：转交 3（第 4 拒）· 加签 3（第 4 拒）·
// 回退 2（第 3 拒）—— 双向：到限前成功、到限即可见拒绝（ErrOpLimitExceeded）。
func TestOpLimitsTransferAddSignRollback(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	// ① 转交上限 3：任务在 ou_m1/ou_m9 之间交替转交（每次转交后新任务即当前任务）。
	t.Run("Transfer上限3", func(t *testing.T) {
		bizNo := submitOneNode(t, svc, "ou_m1")
		cur := "ou_m1"
		other := "ou_m9"
		for i := 1; i <= 3; i++ { // 期望字面 3（FR-M9-04）—— 不引用常量，防实现/测试同步漂移
			tk := pendingTaskFor(t, db, bizNo, cur)
			if err := svc.Transfer(ctx, bizNo, tk.TaskID, cur, other, "接替", "休假"); err != nil {
				t.Fatalf("第 %d 次转交应成功：%v", i, err)
			}
			cur, other = other, cur
		}
		tk := pendingTaskFor(t, db, bizNo, cur)
		err := svc.Transfer(ctx, bizNo, tk.TaskID, cur, other, "接替", "休假")
		if !errors.Is(err, flow.ErrOpLimitExceeded) {
			t.Fatalf("第 4 次转交应 ErrOpLimitExceeded，实为 %v", err)
		}
	})

	// ② 加签上限 3：同一当前任务连续加签（当前任务不因加签结束）。
	t.Run("AddSign上限3", func(t *testing.T) {
		bizNo := submitOneNode(t, svc, "ou_a1")
		tk := taskFor(t, db, bizNo, "ou_a1")
		for i := 1; i <= 3; i++ { // 期望字面 3（FR-M9-05）—— 不引用常量，防实现/测试同步漂移
			if err := svc.AddSign(ctx, bizNo, tk.TaskID, "ou_a1", "ou_a2", "加签人", "", flow.AddSignAfter); err != nil {
				t.Fatalf("第 %d 次加签应成功：%v", i, err)
			}
		}
		err := svc.AddSign(ctx, bizNo, tk.TaskID, "ou_a1", "ou_a2", "加签人", "", flow.AddSignAfter)
		if !errors.Is(err, flow.ErrOpLimitExceeded) {
			t.Fatalf("第 4 次加签应 ErrOpLimitExceeded，实为 %v", err)
		}
	})

	// ③ 回退上限 2：两节点单——n2 活动时回退到 n1，approve n1 推进 n2，再回退……第 3 次拒。
	t.Run("Rollback上限2", func(t *testing.T) {
		bizNo := submitTwoNodes(t, svc, "ou_b1", "ou_b2")
		// n1 approve → n2 活动
		tk1 := taskFor(t, db, bizNo, "ou_b1")
		if err := svc.Approve(ctx, bizNo, tk1.TaskID, "ou_b1", "ok", nil); err != nil {
			t.Fatalf("n1 approve: %v", err)
		}
		for i := 1; i <= 2; i++ { // 期望字面 2（FR-M9-06）—— 不引用常量，防实现/测试同步漂移
			// 回退到 n1（更早节点）
			if err := svc.Rollback(ctx, bizNo, "ou_b2", "n1", "资料有误"); err != nil {
				t.Fatalf("第 %d 次回退应成功：%v", i, err)
			}
			// 重新推进：approve n1 → n2 再活动
			tkA := taskFor(t, db, bizNo, "ou_b1")
			if err := svc.Approve(ctx, bizNo, tkA.TaskID, "ou_b1", "ok", nil); err != nil {
				t.Fatalf("重推进 approve: %v", err)
			}
		}
		err := svc.Rollback(ctx, bizNo, "ou_b2", "n1", "资料有误")
		if !errors.Is(err, flow.ErrOpLimitExceeded) {
			t.Fatalf("第 3 次回退应 ErrOpLimitExceeded，实为 %v", err)
		}
	})
}

// TestTransferAgentAuthorization 代理人正向（FR-M9-04/06）：
// 非本人 + authorizer 放行 ⇒ 转交成功；authorizer 拒/nil ⇒ ErrNotAssignee（双向）；
// AddSign **不走** authorizer（代理人不可加签 —— 恒 true 的门下非本人仍拒）。
func TestTransferAgentAuthorization(t *testing.T) {
	ctx := context.Background()

	t.Run("代理放行", func(t *testing.T) {
		db := newFlowDB(t)
		svc := flow.New(db, "app")
		svc.SetAgentAuthorizer(func(_ context.Context, _ *store.FlowTask, actor string) bool {
			return actor == "ou_agent" // 仅 ou_agent 视为有效代理
		})
		bizNo := submitOneNode(t, svc, "ou_owner")
		tk := taskFor(t, db, bizNo, "ou_owner")
		if err := svc.Transfer(ctx, bizNo, tk.TaskID, "ou_agent", "ou_t", "接替", "代办"); err != nil {
			t.Fatalf("代理人转交应放行：%v", err)
		}
	})

	t.Run("代理拒绝", func(t *testing.T) {
		db := newFlowDB(t)
		svc := flow.New(db, "app")
		svc.SetAgentAuthorizer(func(_ context.Context, _ *store.FlowTask, actor string) bool {
			return actor == "ou_agent"
		})
		bizNo := submitOneNode(t, svc, "ou_owner")
		tk := taskFor(t, db, bizNo, "ou_owner")
		err := svc.Transfer(ctx, bizNo, tk.TaskID, "ou_stranger", "ou_t", "", "")
		if !errors.Is(err, flow.ErrNotAssignee) {
			t.Fatalf("非代理非本人应 ErrNotAssignee，实为 %v", err)
		}
	})

	t.Run("nil门=仅本人", func(t *testing.T) {
		db := newFlowDB(t)
		svc := flow.New(db, "app") // 未注入 ⇒ 现状不放宽
		bizNo := submitOneNode(t, svc, "ou_owner")
		tk := taskFor(t, db, bizNo, "ou_owner")
		err := svc.Transfer(ctx, bizNo, tk.TaskID, "ou_agent", "ou_t", "", "")
		if !errors.Is(err, flow.ErrNotAssignee) {
			t.Fatalf("nil 门下非本人应 ErrNotAssignee，实为 %v", err)
		}
	})

	t.Run("AddSign不走代理门", func(t *testing.T) {
		db := newFlowDB(t)
		svc := flow.New(db, "app")
		svc.SetAgentAuthorizer(func(_ context.Context, _ *store.FlowTask, _ string) bool {
			return true // 恒放行的门也**不得**用于加签
		})
		bizNo := submitOneNode(t, svc, "ou_owner")
		tk := taskFor(t, db, bizNo, "ou_owner")
		err := svc.AddSign(ctx, bizNo, tk.TaskID, "ou_agent", "ou_t", "", "", flow.AddSignAfter)
		if !errors.Is(err, flow.ErrNotAssignee) {
			t.Fatalf("代理人不可加签（01a §4.1）：非本人应 ErrNotAssignee，实为 %v", err)
		}
	})
}
