package flow_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// notify_activated_test.go —— 「新待办产生」通知（FR-M0-17 追加接线；02-UseCase UC-23 通知时机）。
//
// 覆盖五条验收：
//  1. 任务激活 → 通知该 assignee（新审批人），**不是**已通过者；
//  2. HELD（未激活）任务不发通知（顺序会签语义，UC-23）；
//  3. 通知失败（code!=0 模拟）→ 记 FAILED、不影响业务状态推进；
//  4. 幂等：同 (biz_no, task_id) 重复触发不重复发送；
//  5. 回归：既有三类事件（转交/回退/撤回 → 已通过者）行为未被改变。

// recCall 一次发送留痕。
type recCall struct {
	BizNo  string
	Target string
	Event  string
}

// recSender 记录型发送替身（记录 biz/target/event；可按 target 注入失败）。
type recSender struct {
	mu    sync.Mutex
	fail  map[string]bool
	calls []recCall
}

func (f *recSender) Send(_ context.Context, bizNo, target, event string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[target] {
		return errors.New("发送失败(模拟)")
	}
	f.calls = append(f.calls, recCall{BizNo: bizNo, Target: target, Event: event})
	return nil
}

// callsTo 返回发给 target 的调用（拷贝）。
func (f *recSender) callsTo(target string) []recCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recCall
	for _, c := range f.calls {
		if c.Target == target {
			out = append(out, c)
		}
	}
	return out
}

// countEvent 统计发给 target 且事件键以 prefix 开头的调用次数。
func (f *recSender) countEvent(target, prefix string) int {
	n := 0
	for _, c := range f.callsTo(target) {
		if strings.HasPrefix(c.Event, prefix) {
			n++
		}
	}
	return n
}

// notifyRows 读取某单的全部通知行（测试断言用）。
func notifyRows(t *testing.T, db *store.DB, bizNo string) []store.NotifyLog {
	t.Helper()
	rows, err := db.ListNotify(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读取通知流水失败: %v", err)
	}
	return rows
}

// rowsOf 取 target 的全部通知行。
func rowsOf(rows []store.NotifyLog, target string) []store.NotifyLog {
	var out []store.NotifyLog
	for _, r := range rows {
		if r.TargetOpenID == target {
			out = append(out, r)
		}
	}
	return out
}

// submitTwoSeqNodes 提交两节点串行单：n1[m1] → n2[m2, m3]（顺序会签）。
func submitTwoSeqNodes(t *testing.T, svc *flow.Service) string {
	t.Helper()
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "主管", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_m1"}}},
			{NodeID: "n2", NodeName: "总经理", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_m2"}, {OpenID: "ou_m3"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

// TestActivatedNotifyTargets 收件人算法（纯函数）：仅「可办理」任务、排除 actor/空、去重。
func TestActivatedNotifyTargets(t *testing.T) {
	tasks := []store.FlowTask{
		{TaskID: "t1", AssigneeOpenID: "ou_a", Status: flow.TaskPending, ReleaseState: flow.ReleaseReleased},
		{TaskID: "t2", AssigneeOpenID: "ou_b", Status: flow.TaskPending, ReleaseState: flow.ReleaseHeld},       // HELD：不发
		{TaskID: "t3", AssigneeOpenID: "ou_c", Status: flow.TaskApproved, ReleaseState: flow.ReleaseReleased},  // 已通过：不发
		{TaskID: "t4", AssigneeOpenID: "ou_app", Status: flow.TaskPending, ReleaseState: flow.ReleaseReleased}, // 操作人本人：不发
		{TaskID: "t5", AssigneeOpenID: "", Status: flow.TaskPending, ReleaseState: flow.ReleaseReleased},       // 空 assignee：不发
	}
	got := flow.ActivatedNotifyTargets(tasks, "ou_app")
	if len(got) != 1 || got[0].TaskID != "t1" || got[0].AssigneeOpenID != "ou_a" {
		t.Fatalf("ActivatedNotifyTargets = %+v, 期望仅 t1/ou_a（可办理 ∧ 非 actor ∧ 非空）", got)
	}
}

// TestActivatedNotifyOnActivation 验收①②：激活即通知新审批人；HELD 不发；收件人不是已通过者。
func TestActivatedNotifyOnActivation(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	sender := &recSender{}
	svc.Subscribe(flow.NewNotifier(db, sender, nil))

	bizNo := submitTwoSeqNodes(t, svc)

	// ① 提交后：仅 ou_m1（首位，RELEASED）收到 TASK_ACTIVATED；ou_m2/ou_m3（HELD）零通知。
	if got := sender.countEvent("ou_m1", "TASK_ACTIVATED"); got != 1 {
		t.Fatalf("提交后 ou_m1 的 TASK_ACTIVATED = %d, 期望 1", got)
	}
	for _, held := range []string{"ou_m2", "ou_m3"} {
		if got := sender.countEvent(held, "TASK_ACTIVATED"); got != 0 {
			t.Errorf("提交后 HELD 任务 %s 收到通知 %d 次, 期望 0（UC-23：未激活不发通知）", held, got)
		}
		if rows := rowsOf(notifyRows(t, db, bizNo), held); len(rows) != 0 {
			t.Errorf("HELD 任务 %s 不应有任何通知流水, 实际 %d 行", held, len(rows))
		}
	}
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if m1.ReleaseState != flow.ReleaseReleased {
		t.Fatalf("前置不成立：m1 = %s, 期望 RELEASED", m1.ReleaseState)
	}

	// ② m1 同意 → ou_m2 激活收到通知；ou_m3 仍 HELD 不发。
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意"); err != nil {
		t.Fatal(err)
	}
	if got := sender.countEvent("ou_m2", "TASK_ACTIVATED"); got != 1 {
		t.Fatalf("m1 同意后 ou_m2 的 TASK_ACTIVATED = %d, 期望 1", got)
	}
	if got := sender.countEvent("ou_m3", "TASK_ACTIVATED"); got != 0 {
		t.Errorf("m1 同意后 ou_m3（仍 HELD）收到通知 %d 次, 期望 0", got)
	}

	// ③ m2 同意 → ou_m3 激活；★ 断言收件人＝新审批人（ou_m3），不是已通过者（ou_m1/ou_m2
	// 不因「已通过者」口径在激活事件上收到任何 TASK_ACTIVATED）。
	m2 := taskFor(t, db, bizNo, "ou_m2")
	if err := svc.Approve(ctx, bizNo, m2.TaskID, "ou_m2", "同意"); err != nil {
		t.Fatal(err)
	}
	if got := sender.countEvent("ou_m3", "TASK_ACTIVATED"); got != 1 {
		t.Fatalf("m2 同意后 ou_m3 的 TASK_ACTIVATED = %d, 期望 1", got)
	}
	for _, approved := range []string{"ou_m1", "ou_m2"} {
		// ou_m1 的激活发生在提交时、ou_m2 的激活发生在 m1 同意时 —— m2 同意后
		// 二者不应再收到新的 TASK_ACTIVATED（激活口径≠已通过者口径）。
		want := 1
		if approved == "ou_m2" {
			want = 1 // m1 同意时激活的那一次
		}
		if got := sender.countEvent(approved, "TASK_ACTIVATED"); got != want {
			t.Errorf("已通过者 %s 的 TASK_ACTIVATED = %d, 期望 %d（不再因已通过而收到激活通知）", approved, got, want)
		}
	}

	// ④ 流水留痕：三行 TASK_ACTIVATED 均为 SENT、事件键携带 task_id、channel=feishu_bot。
	rows := notifyRows(t, db, bizNo)
	actN := 0
	for _, r := range rows {
		if !strings.HasPrefix(r.Event, "TASK_ACTIVATED") {
			continue
		}
		actN++
		if r.Status != store.NotifySent || r.Channel != flow.ChannelBot {
			t.Errorf("TASK_ACTIVATED 行 status=%s channel=%s, 期望 SENT/feishu_bot", r.Status, r.Channel)
		}
		if !strings.Contains(r.Event, taskFor(t, db, bizNo, r.TargetOpenID).TaskID) {
			t.Errorf("事件键 %s 未携带 %s 的 task_id", r.Event, r.TargetOpenID)
		}
	}
	if actN != 3 {
		t.Fatalf("TASK_ACTIVATED 流水 = %d 行, 期望 3（m1/m2/m3 各一次）", actN)
	}
}

// TestActivatedNotifyFailureNotBlocking 验收③：发送失败 → 记 FAILED、业务照常推进。
func TestActivatedNotifyFailureNotBlocking(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	sender := &recSender{fail: map[string]bool{"ou_m2": true}}
	svc.Subscribe(flow.NewNotifier(db, sender, nil))

	bizNo := submitTwoSeqNodes(t, svc)
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意"); err != nil {
		t.Fatalf("m1 同意失败（通知失败不得影响业务）: %v", err)
	}

	// ou_m2 的激活通知：FAILED + last_error 留痕（漏发可检出）。
	var failed *store.NotifyLog
	for _, r := range rowsOf(notifyRows(t, db, bizNo), "ou_m2") {
		if strings.HasPrefix(r.Event, "TASK_ACTIVATED") {
			failed = &r
		}
	}
	if failed == nil {
		t.Fatal("ou_m2 无 TASK_ACTIVATED 流水, 期望 1 行 FAILED")
	}
	if failed.Status != store.NotifyFailed || failed.LastError == "" {
		t.Errorf("失败通知 status=%s last_error=%q, 期望 FAILED + 非空错误", failed.Status, failed.LastError)
	}

	// 业务不受影响：m2 任务仍 PENDING/RELEASED，且可正常同意 → m3 激活并成功发送。
	m2 := taskFor(t, db, bizNo, "ou_m2")
	if m2.Status != flow.TaskPending || m2.ReleaseState != flow.ReleaseReleased {
		t.Fatalf("通知失败影响了业务：m2 = %s/%s, 期望 PENDING/RELEASED", m2.Status, m2.ReleaseState)
	}
	if err := svc.Approve(ctx, bizNo, m2.TaskID, "ou_m2", "同意"); err != nil {
		t.Fatalf("m2 同意失败: %v", err)
	}
	if got := sender.countEvent("ou_m3", "TASK_ACTIVATED"); got != 1 {
		t.Errorf("m2 同意后 ou_m3 的 TASK_ACTIVATED = %d, 期望 1（状态机继续推进）", got)
	}
	if inst := instOf(t, db, bizNo); inst.Status != flow.InstancePending {
		t.Errorf("实例状态 = %s, 期望仍 PENDING", inst.Status)
	}
}

// TestActivatedNotifyIdempotent 验收④：同 (biz_no, task_id) 重复触发不重复发送。
func TestActivatedNotifyIdempotent(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	sender := &recSender{}
	svc.Subscribe(flow.NewNotifier(db, sender, nil))

	bizNo := submitTwoSeqNodes(t, svc)
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意"); err != nil {
		t.Fatal(err)
	}

	// ① 后置加签（ADDED_SIGN 空转：可办理任务不变）→ 不得给 ou_m1 重发激活提醒；
	//    被加签人 ou_x 的任务 HELD → 也不发。
	m2 := taskFor(t, db, bizNo, "ou_m2")
	if err := svc.AddSign(ctx, bizNo, m2.TaskID, "ou_m2", "ou_x", "小X", "加签", flow.AddSignAfter); err != nil {
		t.Fatal(err)
	}
	if got := sender.countEvent("ou_m1", "TASK_ACTIVATED"); got != 1 {
		t.Errorf("加签后 ou_m1 的 TASK_ACTIVATED = %d, 期望仍 1（幂等不重发）", got)
	}
	if got := sender.countEvent("ou_x", "TASK_ACTIVATED"); got != 0 {
		t.Errorf("被加签人 ou_x（HELD）收到通知 %d 次, 期望 0", got)
	}

	// ② 回退到 n1（ou_m2 操作）→ m1 任务重激活（同 task_id、round+1）→ 不得重发；
	//    m1 作为「已通过者」收到 ROLLED_BACK 知会（既有口径覆盖，不漏知会）。
	if err := svc.Rollback(ctx, bizNo, "ou_m2", "n1", "回退重审"); err != nil {
		t.Fatal(err)
	}
	if got := sender.countEvent("ou_m1", "TASK_ACTIVATED"); got != 1 {
		t.Errorf("回退重激活后 ou_m1 的 TASK_ACTIVATED = %d, 期望仍 1（同 task_id 不重复轰炸）", got)
	}
	if got := sender.countEvent("ou_m1", "ROLLED_BACK"); got != 1 {
		t.Errorf("ou_m1 的 ROLLED_BACK（已通过者口径）= %d, 期望 1", got)
	}
	// m1 任务行仍只有一条 TASK_ACTIVATED 流水。
	actN := 0
	for _, r := range rowsOf(notifyRows(t, db, bizNo), "ou_m1") {
		if strings.HasPrefix(r.Event, "TASK_ACTIVATED") {
			actN++
		}
	}
	if actN != 1 {
		t.Errorf("ou_m1 的 TASK_ACTIVATED 流水 = %d 行, 期望 1", actN)
	}
}

// TestLegacyNotifyEventsUnchanged 验收⑤（回归）：转交/撤回的「已通过者」口径行为未变，
// 且与「新待办」口径在同一事件上并存、事件键互不干扰。
func TestLegacyNotifyEventsUnchanged(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	sender := &recSender{}
	svc.Subscribe(flow.NewNotifier(db, sender, nil))

	// n1[m1] → n2[m2]；m1 通过后 m2 把任务转交给 ou_t，再由申请人撤回。
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_m1"}}},
			{NodeID: "n2", Seq: 2, Approvers: []flow.Approver{{OpenID: "ou_m2"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意"); err != nil {
		t.Fatal(err)
	}
	m2 := taskFor(t, db, bizNo, "ou_m2")
	if err := svc.Transfer(ctx, bizNo, m2.TaskID, "ou_m2", "ou_t", "小T", "转交"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Cancel(ctx, bizNo, "ou_app", "撤回"); err != nil {
		t.Fatal(err)
	}

	rows := notifyRows(t, db, bizNo)
	// ① 既有口径原样：TRANSFERRED/CANCELED 事件键＝纯事件名（不含任务键）、收件人＝已通过者（ou_m1）。
	for _, evType := range []string{"TRANSFERRED", "CANCELED"} {
		var hit int
		for _, r := range rows {
			if r.Event != evType { // ★ 精确相等：事件键形态未被改变
				continue
			}
			hit++
			if r.TargetOpenID != "ou_m1" {
				t.Errorf("%s 收件人 = %s, 期望 ou_m1（已通过者口径不变）", evType, r.TargetOpenID)
			}
			if r.Status != store.NotifySent {
				t.Errorf("%s status = %s, 期望 SENT", evType, r.Status)
			}
		}
		if hit != 1 {
			t.Errorf("%s 流水 = %d 行, 期望 1（既有口径行为不变）", evType, hit)
		}
	}
	// ② 新口径并存：ou_t（接力人）恰好收到一次 TASK_ACTIVATED（携带新任务 task_id）。
	if got := sender.countEvent("ou_t", "TASK_ACTIVATED"); got != 1 {
		t.Errorf("ou_t 的 TASK_ACTIVATED = %d, 期望 1", got)
	}
	// ③ ou_m2 恰好一次激活通知（m1 同意时激活、转交后其任务已 TRANSFERRED 不再激活），
	//    且事件键对应其自己的任务。
	if got := sender.countEvent("ou_m2", "TASK_ACTIVATED"); got != 1 {
		t.Errorf("ou_m2 的 TASK_ACTIVATED = %d, 期望 1（转交后不再激活其旧任务）", got)
	}
}

// TestActivatedNotifyNoSenderKeepsExpected sender=nil → 新待办口径只落 EXPECTED（漏发可检出）。
func TestActivatedNotifyNoSenderKeepsExpected(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	svc.Subscribe(flow.NewNotifier(db, nil, nil)) // 无发送器

	bizNo := submitTwoSeqNodes(t, svc)
	rows := rowsOf(notifyRows(t, db, bizNo), "ou_m1")
	if len(rows) != 1 || rows[0].Status != store.NotifyExpected ||
		!strings.HasPrefix(rows[0].Event, "TASK_ACTIVATED") {
		t.Fatalf("ou_m1 通知行 = %+v, 期望 1 行 EXPECTED/TASK_ACTIVATED（两阶段机制沿用）", rows)
	}
}
