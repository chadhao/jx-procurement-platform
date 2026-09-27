package feishu

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// message_notify_test.go —— 第 3 批（docs/16 §2-F 关联项）通知发送与卡片更新的单测：
//
//	① message/send 成功路径：断言请求体 **四 URL 齐全**、texts 为 **map 形态**、template_id=1008；
//	② message/send `code != 0`（HTTP 200 + {"code":60001,…}）⇒ **必须判为失败**，
//	   t_notify_log 记 FAILED + last_error，**不得当成功**；
//	③ 通知两阶段：EXPECTED → SENT（成功）/ FAILED（失败）状态流转；
//	④ UpdateApprovalMessage / RepairCardFeedback：message_id 为空 ⇒ **不发请求**（Fake 计数为 0）。

var notifyAt = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

// fakeFeishuServer 模拟飞书开放平台：token 端点 + message/send（捕获请求体）。
type fakeFeishuServer struct {
	mu sync.Mutex
	// sendCode / sendMsg message/send 的响应 code/msg（模拟 60001 等 code!=0 仍 HTTP 200 的实测坑）。
	sendCode int
	sendMsg  string
	// sendBodies 收到的 message/send 请求体（原始 JSON）。
	sendBodies []string
	srv        *httptest.Server
}

func newFakeFeishuServer(t *testing.T, sendCode int, sendMsg string) *fakeFeishuServer {
	t.Helper()
	f := &fakeFeishuServer{sendCode: sendCode, sendMsg: sendMsg}
	mux := http.NewServeMux()
	// tenant_access_token（tokenManager 依赖；测试环境直接放行）。
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
	})
	mux.HandleFunc("/open-apis/approval/v1/message/send", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.sendBodies = append(f.sendBodies, string(raw))
		code, msg := f.sendCode, f.sendMsg
		f.mu.Unlock()
		// ★ 实测坑：HTTP 恒 200，成败看 body 的 code。
		_, _ = io.WriteString(w, `{"code":`+strconv.Itoa(code)+`,"msg":"`+msg+`","data":{"message_id":"om_fake_1"}}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// newTestHTTPClient 构造指向 fake 服务器的 HTTPClient（同包改写未导出 baseURL，含 tokenManager）。
func newTestHTTPClient(t *testing.T, f *fakeFeishuServer) *HTTPClient {
	t.Helper()
	c := NewHTTPClient("app-test", "secret-test", nil, nil)
	c.baseURL = f.srv.URL
	c.tokens.baseURL = f.srv.URL
	return c
}

// seedNotifyInstance 建一条实例（含申请人）供 Send 定位。
func seedNotifyInstance(t *testing.T, db *store.DB) string {
	t.Helper()
	const bizNo = "PR-2609-0100"
	if err := db.UpsertInstance(context.Background(), &store.Instance{
		InstanceCode: "app-test:" + bizNo, ApprovalCode: "code-pr", DocType: "PR", BizNo: bizNo,
		Status: "PENDING", ApplicantOpenID: "ou_applicant", ApplicantName: "张三",
		UpdateTime: 1, CreatedAt: notifyAt, UpdatedAt: notifyAt,
	}); err != nil {
		t.Fatal(err)
	}
	return bizNo
}

// emitTransferredNotify 模拟一次「转交」流程事件（Notifier 先落 EXPECTED 再发送）。
func emitTransferredNotify(t *testing.T, db *store.DB, sender flow.Sender, bizNo string) {
	t.Helper()
	flow.NewNotifier(db, sender, nil).OnFlowEvent(context.Background(), flow.FlowEvent{
		Type: flow.EventTransferred, BizNo: bizNo, DocType: "PR", ActorOpenID: "ou_operator",
		NotifyTargets: []string{"ou_target"},
	})
}

// TestNotifySenderSuccessPayloadMeasuredContract 成功路径：请求体按实测契约断言
// （template_id=1008、四 URL 齐全、texts 为 map、确定性 uuid），且两阶段 EXPECTED→SENT。
func TestNotifySenderSuccessPayloadMeasuredContract(t *testing.T) {
	f := newFakeFeishuServer(t, 0, "success")
	db := storetest.NewDB(t)
	bizNo := seedNotifyInstance(t, db)
	sender := NewNotifySender(db, newTestHTTPClient(t, f), "https://jx.example.com", nil)

	emitTransferredNotify(t, db, sender, bizNo)

	// ① 两阶段回填：SENT=1、无未发送（漏发可检出口径）。
	if n, _ := db.CountNotifyByStatus(context.Background(), bizNo, store.NotifySent); n != 1 {
		t.Fatalf("SENT = %d, 期望 1（EXPECTED→SENT 流转）", n)
	}
	if unsent, _ := db.ListUnsentNotify(context.Background(), bizNo); len(unsent) != 0 {
		t.Fatalf("未发送通知 = %+v, 期望空", unsent)
	}
	// ② 请求体实测契约。
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendBodies) != 1 {
		t.Fatalf("message/send 调用次数 = %d, 期望 1", len(f.sendBodies))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(f.sendBodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["template_id"] != NotifyTemplateTodo { // "1008"
		t.Errorf("template_id = %v, 期望 %q（收到审批待办）", body["template_id"], NotifyTemplateTodo)
	}
	if body["open_id"] != "ou_target" || body["title_user_id"] != "ou_applicant" {
		t.Errorf("open_id/title_user_id = %v/%v, 期望 ou_target/ou_applicant", body["open_id"], body["title_user_id"])
	}
	// ★ actions[] 四个 URL 缺一不可（缺一 ⇒ 60001 actionUrls incomplete error，HTTP 仍 200）。
	actions, ok := body["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("actions = %#v, 期望 1 项", body["actions"])
	}
	act, _ := actions[0].(map[string]any)
	detail := "https://jx.example.com/approval/" + bizNo
	for _, k := range []string{"url", "pc_url", "android_url", "ios_url"} {
		if v, _ := act[k].(string); v != detail {
			t.Errorf("actions[0].%s = %v, 期望 %q（四 URL 必须齐全）", k, act[k], detail)
		}
	}
	// ★ 本接口 texts 是 map 形态（与 external_approvals 的数组形态相反）。
	res, _ := body["i18n_resources"].([]any)
	if len(res) != 1 {
		t.Fatalf("i18n_resources = %#v, 期望 1 项", body["i18n_resources"])
	}
	texts, ok := res[0].(map[string]any)["texts"].(map[string]any)
	if !ok {
		t.Fatalf("texts = %#v, 期望 map 形态（实测契约，勿用数组）", res[0].(map[string]any)["texts"])
	}
	if texts["@i18n@name"] == "" || texts["@i18n@s1"] == "" || texts["@i18n@s2"] == "" {
		t.Errorf("texts 缺 @i18n@ 键: %#v", texts)
	}
	// 确定性 uuid（幂等：同 uuid 一小时内只发一次）。
	if want := sender.uuid(bizNo, "ou_target", "TRANSFERRED"); body["uuid"] != want {
		t.Errorf("uuid = %v, 期望确定性值 %q", body["uuid"], want)
	}
}

// TestNotifySenderNonZeroCodeIsFailure code!=0（HTTP 200）⇒ 判失败：
// Send 返回 error，t_notify_log 记 FAILED + last_error（绝不因 HTTP 200 当成功）。
func TestNotifySenderNonZeroCodeIsFailure(t *testing.T) {
	f := newFakeFeishuServer(t, 60001, "60001 actionUrls incomplete error")
	db := storetest.NewDB(t)
	bizNo := seedNotifyInstance(t, db)
	sender := NewNotifySender(db, newTestHTTPClient(t, f), "https://jx.example.com", nil)

	// 两阶段：EXPECTED → FAILED。
	emitTransferredNotify(t, db, sender, bizNo)

	if n, _ := db.CountNotifyByStatus(context.Background(), bizNo, store.NotifyFailed); n != 1 {
		t.Fatalf("FAILED = %d, 期望 1（code!=0 必须判失败，不得当成功）", n)
	}
	if n, _ := db.CountNotifyByStatus(context.Background(), bizNo, store.NotifySent); n != 0 {
		t.Fatalf("SENT = %d, 期望 0", n)
	}
	unsent, err := db.ListUnsentNotify(context.Background(), bizNo)
	if err != nil || len(unsent) != 1 {
		t.Fatalf("未发送通知 = %+v, err=%v（漏发可检出）", unsent, err)
	}
	if !strings.Contains(unsent[0].LastError, "60001") {
		t.Errorf("last_error = %q, 期望含 60001（错误码留痕）", unsent[0].LastError)
	}
	// 直接调 Send 也必须返回 error（端口层语义）。
	if err := sender.Send(context.Background(), bizNo, "ou_target", "TRANSFERRED"); err == nil {
		t.Errorf("code=60001 时 Send 应返回 error（HTTP 200 不代表成功）")
	}
}

// TestNotifySenderMissingDomainFailsVisibly 未配 JX_CALLBACK_DOMAIN ⇒ 可见失败（不编造 URL）。
func TestNotifySenderMissingDomainFailsVisibly(t *testing.T) {
	f := newFakeFeishuServer(t, 0, "success")
	db := storetest.NewDB(t)
	bizNo := seedNotifyInstance(t, db)
	sender := NewNotifySender(db, newTestHTTPClient(t, f), "", nil)

	if err := sender.Send(context.Background(), bizNo, "ou_target", "TRANSFERRED"); err == nil {
		t.Fatalf("未配置详情域名时应可见失败（四 URL 缺一即 60001，不得静默编造）")
	}
	f.mu.Lock()
	n := len(f.sendBodies)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("不应发出任何请求，实际 %d 次", n)
	}
}

// TestRepairCardFeedbackSkipsEmptyMessageID message_id 为空 ⇒ 不发同步请求（Fake 计数为 0）。
func TestRepairCardFeedbackSkipsEmptyMessageID(t *testing.T) {
	fake := NewFakeMessageClient()
	fb := NewRepairCardFeedback(fake, slog.Default())

	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 1, "", context.Canceled)
	if fake.UpdateCount() != 0 {
		t.Fatalf("message_id 为空时不得发请求，UpdateCount = %d, 期望 0", fake.UpdateCount())
	}
}

// TestRepairCardFeedbackMarksOnce 卡片反馈：非空 message_id 调 update；
// 同 (biz_no, task_id, round) 只成功标注一次（防 30s 循环重复刷卡片）。
func TestRepairCardFeedbackMarksOnce(t *testing.T) {
	fake := NewFakeMessageClient()
	fb := NewRepairCardFeedback(fake, slog.Default())

	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 1, "om_1", context.Canceled)
	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 1, "om_1", context.Canceled)
	if got := fake.UpdateCount(); got != 1 {
		t.Fatalf("同 key 应只标注一次，UpdateCount = %d, 期望 1", got)
	}
	ups := fake.Updates()
	if ups[0].MessageID != "om_1" {
		t.Errorf("message_id = %q, 期望 om_1（用落盘的 t_flow_op_log.message_id）", ups[0].MessageID)
	}
	if ups[0].Body["message_id"] != "om_1" || ups[0].Body["content"] == "" {
		t.Errorf("候选请求体不完整: %#v（字段结构待 V-2 实测校准）", ups[0].Body)
	}
	// 不同 round 视为新事件，允许再次标注（回退重审后是新的卡片交互轮次）。
	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 2, "om_2", context.Canceled)
	if got := fake.UpdateCount(); got != 2 {
		t.Fatalf("不同 round 应再标注，UpdateCount = %d, 期望 2", got)
	}
}

// TestRepairCardFeedbackUpdateFailureNoRollback update 失败 ⇒ 只记日志不 panic、
// 不标记 done（下一轮修复循环重试标注），不影响调用方（修复循环）。
func TestRepairCardFeedbackUpdateFailureNoRollback(t *testing.T) {
	attempts := 0
	succeed := false
	fake := NewFakeMessageClient()
	fake.UpdateFn = func(context.Context, string, map[string]any) error {
		attempts++
		if !succeed {
			return context.DeadlineExceeded
		}
		return nil
	}
	fb := NewRepairCardFeedback(fake, observ.NewLogger("error", nil))

	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 1, "om_1", context.Canceled)
	if attempts != 1 {
		t.Fatalf("失败后应已尝试一次，attempts = %d", attempts)
	}
	// ★ 失败不标记 done ⇒ 下一轮（成功后）重试标注，同 key 的防重只在成功后生效。
	succeed = true
	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 1, "om_1", context.Canceled)
	if attempts != 2 {
		t.Fatalf("失败不应标记 done（下一轮须重试），attempts = %d, 期望 2", attempts)
	}
	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 1, "om_1", context.Canceled)
	if attempts != 2 {
		t.Fatalf("成功后才防重（同 key 不再标注），attempts = %d, 期望 2", attempts)
	}
}

// TestHTTPClientUpdateApprovalMessage 端口实现：路径正确；空 message_id 不发请求。
func TestHTTPClientUpdateApprovalMessage(t *testing.T) {
	var mu sync.Mutex
	updates := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
	})
	mux.HandleFunc("/open-apis/approval/v1/message/update", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		updates++
		mu.Unlock()
		_, _ = io.WriteString(w, `{"code":0,"msg":"success","data":{}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewHTTPClient("app-test", "secret-test", nil, nil)
	c.baseURL = srv.URL
	c.tokens.baseURL = srv.URL

	// ★ 空 message_id ⇒ 报错且不发请求（无卡可更新）。
	if err := c.UpdateApprovalMessage(context.Background(), "  ", map[string]any{}); err == nil {
		t.Fatalf("空 message_id 应报错")
	}
	mu.Lock()
	n := updates
	mu.Unlock()
	if n != 0 {
		t.Fatalf("空 message_id 不得发请求，实际 %d 次", n)
	}
	if err := c.UpdateApprovalMessage(context.Background(), "om_1", map[string]any{"message_id": "om_1"}); err != nil {
		t.Fatalf("正常更新不应报错: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if updates != 1 {
		t.Errorf("update 调用次数 = %d, 期望 1", updates)
	}
}
