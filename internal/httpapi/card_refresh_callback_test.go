package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// card_refresh_callback_test.go —— 回调成功 ⇒ 卡片主动刷新（HTTP 级契约，本批核心）：
//
//	实测背景：回调处理成功后平台**不自动刷新** Bot 卡片（仍带「同意/拒绝」两键）；
//	卡片操作回调报文**不带** message_id ⇒ 刷新所需的卡片 id 取自我方发送时落盘的
//	t_notify_log.message_id（0014 列）。
//
//	★ 纪律断言（#69「落盘即 200」）：**刷新卡片失败绝不影响回调的 200**。

// cardRefreshSubscriberForTest 与 bootstrap.flowCardRefreshSubscriber 同构的适配器
// （bootstrap 侧不导出，测试就地复刻同一映射：TASK_APPROVED→APPROVED / TASK_REJECTED→REJECTED）。
type cardRefreshSubscriberForTest struct {
	refresher *feishu.CardRefresher
}

// OnFlowEvent 实现 flow.Subscriber。
func (s *cardRefreshSubscriberForTest) OnFlowEvent(ctx context.Context, ev flow.FlowEvent) {
	if s.refresher == nil {
		return
	}
	switch ev.Type {
	case flow.EventTaskApproved:
		s.refresher.Refresh(ctx, ev.BizNo, ev.TaskID, "APPROVED")
	case flow.EventTaskRejected:
		s.refresher.Refresh(ctx, ev.BizNo, ev.TaskID, "REJECTED")
	}
}

// seedCardRefreshFixture 预置实例/任务/回调探针，并为我方发出过的待办卡片落 message_id，
// 返回 (bizNo, taskID)。
func seedCardRefreshFixture(t *testing.T, svc *flow.Service, db *store.DB) (string, string) {
	t.Helper()
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)
	ctx := context.Background()
	event := "TASK_ACTIVATED:" + taskID
	if err := db.EnsureNotifyExpected(ctx, &store.NotifyLog{
		BizNo: bizNo, TargetOpenID: "ou_m1", Channel: "feishu_bot",
		Event: event, Status: store.NotifySent,
	}); err != nil {
		t.Fatalf("预置待办通知行失败: %v", err)
	}
	if err := db.UpdateNotifyMessageID(ctx, bizNo, "ou_m1", event, "feishu_bot", "om_card_1"); err != nil {
		t.Fatalf("预置 message_id 失败: %v", err)
	}
	return bizNo, taskID
}

// wireCardRefreshProbe 装配：真实 advancer（与 bootstrap 同构）+ 卡片刷新订阅者。
func wireCardRefreshProbe(svc *flow.Service, db *store.DB, fake *feishu.FakeMessageClient) {
	svc.SetCallbackAdvancer(func(ctx context.Context, req flow.CallbackRequest) error {
		switch strings.ToUpper(strings.TrimSpace(req.OpType)) {
		case flow.OpApprove:
			return svc.Approve(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason, nil)
		case flow.OpReject:
			return svc.Reject(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason)
		default:
			return nil
		}
	})
	svc.Subscribe(&cardRefreshSubscriberForTest{refresher: feishu.NewCardRefresher(db, fake, nil)})
}

// TestCallbackSuccessTriggersCardRefresh 回调成功（推进成功）⇒ 触发卡片刷新：
// UpdateApprovalMessage 被调用、message_id 取自 t_notify_log、status=APPROVED。
func TestCallbackSuccessTriggersCardRefresh(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCardRefreshFixture(t, svc, db)
	fake := feishu.NewFakeMessageClient()
	wireCardRefreshProbe(svc, db, fake)

	e := newCallbackProbeApp(t, svc, db)
	body := `{"action_type":"APPROVE","biz_no":"` + bizNo + `","task_id":"` + taskID +
		`","token":"tok-pr","reason":"同意","operator":{"open_id":"ou_m1"}}`
	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("回调成功应回 200，实际 http=%d body=%s", rec.Code, rec.Body.String())
	}
	if env.Code != codeOK {
		t.Fatalf("成功回调应 code=0，实际 %d body=%s", env.Code, rec.Body.String())
	}
	ups := fake.Updates()
	if len(ups) != 1 {
		t.Fatalf("应恰好触发一次卡片刷新，实际 %d 次", len(ups))
	}
	if ups[0].MessageID != "om_card_1" {
		t.Errorf("message_id = %q, 期望 t_notify_log 落盘的 om_card_1", ups[0].MessageID)
	}
	if ups[0].Status != "APPROVED" {
		t.Errorf("status = %q, 期望 APPROVED（与审批终态一致）", ups[0].Status)
	}
}

// TestCardRefreshFailureStillReturns200 刷新卡片失败 ⇒ 回调**仍返回 200**
// （#69「落盘即 200」关键纪律断言：卡片刷新是旁路反馈，绝不影响回调成败）。
func TestCardRefreshFailureStillReturns200(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCardRefreshFixture(t, svc, db)
	fake := feishu.NewFakeMessageClient()
	fake.UpdateFn = func(context.Context, string, string) error {
		return errors.New("模拟 message/update 失败")
	}
	wireCardRefreshProbe(svc, db, fake)

	e := newCallbackProbeApp(t, svc, db)
	body := `{"action_type":"APPROVE","biz_no":"` + bizNo + `","task_id":"` + taskID +
		`","token":"tok-pr","reason":"同意","operator":{"open_id":"ou_m1"}}`
	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("刷新失败不得影响回调 200（#69 落盘即 200），实际 http=%d body=%s",
			rec.Code, rec.Body.String())
	}
	if env.Code != codeOK {
		t.Fatalf("回调仍应 code=0，实际 %d", env.Code)
	}
	// 状态机推进不受刷新失败影响：任务已 APPROVED。
	task, err := db.GetFlowTask(context.Background(), taskID)
	if err != nil || task.Status != flow.TaskApproved {
		t.Fatalf("刷新失败时任务应已正常推进为 APPROVED（status=%v err=%v）", task, err)
	}
}

// TestCallbackNoCardRecordSkipsRefresh 查不到 message_id（如手工推的调试卷）
// ⇒ 跳过刷新且回调正常 200（不报错、不重复尝试）。
func TestCallbackNoCardRecordSkipsRefresh(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)
	fake := feishu.NewFakeMessageClient()
	wireCardRefreshProbe(svc, db, fake) // 不预置任何通知行

	e := newCallbackProbeApp(t, svc, db)
	body := `{"action_type":"APPROVE","biz_no":"` + bizNo + `","task_id":"` + taskID +
		`","token":"tok-pr","reason":"同意","operator":{"open_id":"ou_m1"}}`
	rec, _ := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("无卡片记录应跳过刷新且回调 200，实际 http=%d", rec.Code)
	}
	if fake.UpdateCount() != 0 {
		t.Fatalf("查不到 message_id 应跳过（0 次刷新请求），实际 %d 次", fake.UpdateCount())
	}
}
