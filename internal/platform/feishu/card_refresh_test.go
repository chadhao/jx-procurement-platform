package feishu

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// card_refresh_test.go —— 推进成功后卡片主动刷新（CardRefresher）单测：
//
//	① 有我方发出的待办卡片记录（t_notify_log.message_id，0014）⇒ 调 message/update，
//	   message_id / status 断言正确；同 (biz_no, task_id, status) 幂等只刷一次；
//	② 查不到 message_id（无通知行 / 行存在但 message_id 为空，如手工推的调试卷）
//	   ⇒ 跳过且不报错（0 次请求）；
//	③ 刷新失败 ⇒ 只记日志不 panic，不标记 done（下次触发重试）；
//	④ 任务归属校验：task_id 不属于该 biz_no ⇒ 跳过（防串单）。

// seedRefreshFixture 预置实例 + 任务 + 待办通知行（含/不含 message_id），返回 bizNo/taskID。
func seedRefreshFixture(t *testing.T, db *store.DB, withMessageID bool) (string, string) {
	t.Helper()
	ctx := context.Background()
	const bizNo = "PR-2609-0200"
	taskID := bizNo + "-n1-ou_m1-1-1"
	if err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "app-test:" + bizNo, ApprovalCode: "code-pr", DocType: "PR", BizNo: bizNo,
		Status: "PENDING", ApplicantOpenID: "ou_applicant",
		UpdateTime: 1, CreatedAt: notifyAt, UpdatedAt: notifyAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		return db.UpsertFlowTaskTx(ctx, tx, &store.FlowTask{
			TaskID: taskID, BizNo: bizNo, NodeID: "n1", NodeName: "主管", NodeSeq: 1,
			Round: 1, AssigneeOpenID: "ou_m1", Status: "APPROVED", ReleaseState: "RELEASED",
			TaskOrder: 1, CreatedAt: notifyAt, UpdatedAt: notifyAt,
		})
	}); err != nil {
		t.Fatal(err)
	}
	event := notifyTodoEventPrefix + ":" + taskID
	if err := db.EnsureNotifyExpected(ctx, &store.NotifyLog{
		BizNo: bizNo, TargetOpenID: "ou_m1", Channel: notifyChannelBot,
		Event: event, Status: store.NotifySent,
	}); err != nil {
		t.Fatal(err)
	}
	if withMessageID {
		if err := db.UpdateNotifyMessageID(ctx, bizNo, "ou_m1", event, notifyChannelBot, "om_card_1"); err != nil {
			t.Fatal(err)
		}
	}
	return bizNo, taskID
}

// TestCardRefresherRefreshesAfterAdvance 推进成功 ⇒ 触发卡片刷新：
// UpdateApprovalMessage 被调用且 message_id 取自 t_notify_log、status 正确；
// 同 (biz_no, task_id, status) 幂等（第二次不重复调）。
func TestCardRefresherRefreshesAfterAdvance(t *testing.T) {
	db := storetest.NewDB(t)
	bizNo, taskID := seedRefreshFixture(t, db, true)
	fake := NewFakeMessageClient()
	r := NewCardRefresher(db, fake, nil)

	r.Refresh(context.Background(), bizNo, taskID, "APPROVED")

	if got := fake.UpdateCount(); got != 1 {
		t.Fatalf("应触发一次卡片刷新，UpdateCount = %d, 期望 1", got)
	}
	up := fake.Updates()[0]
	if up.MessageID != "om_card_1" {
		t.Errorf("message_id = %q, 期望 t_notify_log 落盘的 om_card_1", up.MessageID)
	}
	if up.Status != "APPROVED" {
		t.Errorf("status = %q, 期望 APPROVED（与审批终态一致）", up.Status)
	}

	// ★ 幂等：同 (biz_no, task_id, status) 第二次触发不重复刷新。
	r.Refresh(context.Background(), bizNo, taskID, "APPROVED")
	if got := fake.UpdateCount(); got != 1 {
		t.Fatalf("同 key 应幂等只刷一次，UpdateCount = %d, 期望 1", got)
	}
}

// TestCardRefresherSkipsWhenNoMessageID 查不到 message_id ⇒ 跳过且不报错：
// ① 无通知行；② 通知行存在但 message_id 为空（非我方 message/send 发出的卡片）。
// 两者均 0 次请求（不得对空 message_id 发请求）。
func TestCardRefresherSkipsWhenNoMessageID(t *testing.T) {
	db := storetest.NewDB(t)
	fake := NewFakeMessageClient()
	r := NewCardRefresher(db, fake, nil)

	// ① 完全无 fixture（无实例/任务/通知行）⇒ 一路跳过、0 请求、无 panic。
	r.Refresh(context.Background(), "PR-2609-9999", "t-none", "APPROVED")
	if got := fake.UpdateCount(); got != 0 {
		t.Fatalf("无通知行应跳过，UpdateCount = %d, 期望 0", got)
	}

	// ② 任务/通知行都在，但 message_id 为空（手工推的调试卷形态）⇒ 跳过、0 请求。
	bizNo, taskID := seedRefreshFixture(t, db, false)
	r.Refresh(context.Background(), bizNo, taskID, "APPROVED")
	if got := fake.UpdateCount(); got != 0 {
		t.Fatalf("message_id 为空应跳过，UpdateCount = %d, 期望 0", got)
	}
}

// TestCardRefresherFailureOnlyLogs 刷新失败 ⇒ 只记日志不 panic、不标记 done
// （下次触发重试），绝不向调用方抛错（emit 纪律 / 落盘即 200）。
func TestCardRefresherFailureOnlyLogs(t *testing.T) {
	db := storetest.NewDB(t)
	bizNo, taskID := seedRefreshFixture(t, db, true)
	var calls atomic.Int64
	fake := NewFakeMessageClient()
	fake.UpdateFn = func(context.Context, string, string) error {
		calls.Add(1)
		return errors.New("模拟 message/update 失败")
	}
	r := NewCardRefresher(db, fake, nil)

	r.Refresh(context.Background(), bizNo, taskID, "APPROVED") // 失败：不 panic 即契约
	r.Refresh(context.Background(), bizNo, taskID, "APPROVED") // 未标记 done ⇒ 重试
	if got := calls.Load(); got != 2 {
		t.Fatalf("失败不应标记 done（须可重试），调用次数 = %d, 期望 2", got)
	}
}

// TestCardRefresherRejectsCrossBiz 任务不属于该 biz_no（串单）⇒ 跳过、0 请求。
func TestCardRefresherRejectsCrossBiz(t *testing.T) {
	db := storetest.NewDB(t)
	_, taskID := seedRefreshFixture(t, db, true)
	fake := NewFakeMessageClient()
	r := NewCardRefresher(db, fake, nil)

	r.Refresh(context.Background(), "PR-OTHER", taskID, "APPROVED")
	if got := fake.UpdateCount(); got != 0 {
		t.Fatalf("串单任务应跳过，UpdateCount = %d, 期望 0", got)
	}
}
