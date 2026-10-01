package flow_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// fakeSender 通知发送替身（可对指定 target 失败）。
type fakeSender struct {
	fail map[string]bool
	sent []string
}

func (f *fakeSender) Send(_ context.Context, _ string, target, _ string) error {
	if f.fail[target] {
		return errors.New("发送失败(模拟)")
	}
	f.sent = append(f.sent, target)
	return nil
}

// TestExpectedNotifyTargets 应有集合：只含 APPROVED、去重、排除操作人本人。
func TestExpectedNotifyTargets(t *testing.T) {
	tasks := []store.FlowTask{
		{AssigneeOpenID: "ou_a", Status: flow.TaskApproved},
		{AssigneeOpenID: "ou_b", Status: flow.TaskApproved},
		{AssigneeOpenID: "ou_a", Status: flow.TaskApproved}, // 重复
		{AssigneeOpenID: "ou_c", Status: flow.TaskPending},  // 未通过
		{AssigneeOpenID: "ou_d", Status: flow.TaskRejected}, // 驳回
		{AssigneeOpenID: "ou_e", Status: flow.TaskTransferred},
	}
	got := flow.ExpectedNotifyTargets(tasks, "ou_b") // actor=ou_b 应被排除
	want := []string{"ou_a"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("应有集合 = %v, 期望 %v（去重 + 排除 actor + 仅 APPROVED）", got, want)
	}
}

// TestNotifyMissingDetectable 漏发可检：应发 N=2 只发 M=1 → 未变 SENT 的记录可被检出。
func TestNotifyMissingDetectable(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	// 两节点：n1[m1] → n2[m2, m3]；通过 m1、m2 后，APPROVED={m1,m2}，实例仍 PENDING（n2 待 m3）。
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_m1"}}},
			{NodeID: "n2", Seq: 2, Approvers: []flow.Approver{{OpenID: "ou_m2"}, {OpenID: "ou_m3"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	m1 := taskFor(t, db, bizNo, "ou_m1")
	_ = svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil)
	m2 := taskFor(t, db, bizNo, "ou_m2")
	_ = svc.Approve(ctx, bizNo, m2.TaskID, "ou_m2", "同意", nil)

	// 订阅 notifier：对 ou_m2 发送失败（模拟"应发 2 只发出 1"）。
	sender := &fakeSender{fail: map[string]bool{"ou_m2": true}}
	svc.Subscribe(flow.NewNotifier(db, sender, nil))

	// 触发"通知已通过者"的事件：撤回（actor=ou_app）。
	if err := svc.Cancel(ctx, bizNo, "ou_app", "撤回"); err != nil {
		t.Fatal(err)
	}

	// 应有集合 = {ou_m1, ou_m2}；SENT=1（m1），FAILED=1（m2）。
	sentN, _ := db.CountNotifyByStatus(ctx, bizNo, store.NotifySent)
	failN, _ := db.CountNotifyByStatus(ctx, bizNo, store.NotifyFailed)
	if sentN != 1 || failN != 1 {
		t.Fatalf("通知计数 SENT=%d FAILED=%d, 期望 1/1", sentN, failN)
	}
	unsent, err := db.ListUnsentNotify(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(unsent) != 1 || unsent[0].TargetOpenID != "ou_m2" {
		t.Fatalf("未成功发送 = %+v, 期望仅 ou_m2（漏发可检出）", unsent)
	}
	var got []string
	for _, a := range sender.sent {
		got = append(got, a)
	}
	sort.Strings(got)
	if len(got) != 1 || got[0] != "ou_m1" {
		t.Errorf("已发送 = %v, 期望 [ou_m1]", got)
	}
}

// TestNotifyNoSenderKeepsExpected sender=nil → 只落 EXPECTED（供重试/告警），可被检出。
func TestNotifyNoSenderKeepsExpected(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_m2")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	_ = svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil)
	svc.Subscribe(flow.NewNotifier(db, nil, nil)) // 无发送器
	_ = svc.Cancel(ctx, bizNo, "ou_app", "撤回")
	expN, _ := db.CountNotifyByStatus(ctx, bizNo, store.NotifyExpected)
	if expN != 1 {
		t.Errorf("EXPECTED = %d, 期望 1（ou_m1 已通过）", expN)
	}
}
