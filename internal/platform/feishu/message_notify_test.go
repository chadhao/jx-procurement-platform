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
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// message_notify_test.go —— 第 3 批（docs/16 §2-F 关联项）通知发送与卡片更新的单测
//（本批按 2026-09-28 实测结论更新：message/update body 定稿、message_id 落 t_notify_log）：
//
//	① message/send 成功路径：断言请求体 **四 URL 齐全**、texts 为 **map 形态**、template_id=1008；
//	② message/send `code != 0`（HTTP 200 + {"code":60001,…}）⇒ **必须判为失败**，
//	   t_notify_log 记 FAILED + last_error，**不得当成功**；
//	③ 通知两阶段：EXPECTED → SENT（成功）/ FAILED（失败）状态流转；
//	⑤ message/send 成功 ⇒ 回执 message_id 落 t_notify_log（0014 列；失败 ⇒ 不落）；
//	④ UpdateApprovalMessage（实测定稿 body={"message_id","status"}）/ RepairCardFeedback
//	  （失败态标注暂缓，0 请求）：message_id 为空 ⇒ **不发请求**（Fake 计数为 0）。

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

// TestRepairCardFeedbackSuspendsUpdate 失败态标注**暂缓调用**（2026-09-28 实测定稿）：
// message/update 实测 body={"message_id","status"}，原 {"message_id","content"} 候选体
// 在真机必 60001（缺 status），且「失败态」status 取值未实测（标终态会伪造审批结果）
// ⇒ 不发任何请求（0 次），可见告警取代必败调用。
func TestRepairCardFeedbackSuspendsUpdate(t *testing.T) {
	fake := NewFakeMessageClient()
	fb := NewRepairCardFeedback(fake, slog.Default())

	// 非空 message_id 也不得再发请求（旧实现会发 {"message_id","content"} → 真机必败）。
	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 1, "om_1", context.Canceled)
	fb.OnRepairFailure(context.Background(), "PR-1", "t-1", 2, "om_2", context.Canceled)
	if got := fake.UpdateCount(); got != 0 {
		t.Fatalf("失败态标注已暂缓，不得发请求；UpdateCount = %d, 期望 0", got)
	}
}

// TestNotifySenderRecordsMessageID message/send 成功 ⇒ 回执 data.message_id 落
// t_notify_log.message_id（0014 列写入者；卡片操作的回调不带 message_id，
// 卡片刷新只能靠本列定位卡片）。
func TestNotifySenderRecordsMessageID(t *testing.T) {
	f := newFakeFeishuServer(t, 0, "success")
	db := storetest.NewDB(t)
	bizNo := seedNotifyInstance(t, db)
	sender := NewNotifySender(db, newTestHTTPClient(t, f), "https://jx.example.com", nil)

	emitTransferredNotify(t, db, sender, bizNo)

	rows, err := db.ListNotify(context.Background(), bizNo)
	if err != nil || len(rows) != 1 {
		t.Fatalf("通知行 = %+v, err=%v, 期望恰 1 行", rows, err)
	}
	if rows[0].MessageID != "om_fake_1" {
		t.Fatalf("t_notify_log.message_id = %q, 期望回执值 om_fake_1", rows[0].MessageID)
	}
}

// TestNotifySenderFailureLeavesMessageIDEmpty 发送失败（code!=0）⇒ message_id 不落
// （没有成功发出的消息，无卡可刷；状态仍为 FAILED，漏发可检出）。
func TestNotifySenderFailureLeavesMessageIDEmpty(t *testing.T) {
	f := newFakeFeishuServer(t, 60001, "60001 actionUrls incomplete error")
	db := storetest.NewDB(t)
	bizNo := seedNotifyInstance(t, db)
	sender := NewNotifySender(db, newTestHTTPClient(t, f), "https://jx.example.com", nil)

	emitTransferredNotify(t, db, sender, bizNo)

	rows, err := db.ListNotify(context.Background(), bizNo)
	if err != nil || len(rows) != 1 {
		t.Fatalf("通知行 = %+v, err=%v, 期望恰 1 行", rows, err)
	}
	if rows[0].MessageID != "" {
		t.Fatalf("失败行 message_id 应为空，实际 %q", rows[0].MessageID)
	}
	if rows[0].Status != store.NotifyFailed {
		t.Fatalf("状态 = %s, 期望 FAILED", rows[0].Status)
	}
}

// TestHTTPClientUpdateApprovalMessage 端口实现（2026-09-28 实测定稿）：
// 路径正确；请求体**恰为** {"message_id":…,"status":…} 两键；空 message_id / 空 status
// 均报错且 0 次请求（缺 status 真机必 60001，不发必败请求）。
func TestHTTPClientUpdateApprovalMessage(t *testing.T) {
	var mu sync.Mutex
	updates := 0
	var rawBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
	})
	mux.HandleFunc("/open-apis/approval/v1/message/update", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		updates++
		rawBody = string(b)
		mu.Unlock()
		// ★ 实测成功形态：{"code":0,"data":{"message_id":…},"success"}。
		_, _ = io.WriteString(w, `{"code":0,"msg":"success","data":{"message_id":"om_1"},"success":true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewHTTPClient("app-test", "secret-test", nil, nil)
	c.baseURL = srv.URL
	c.tokens.baseURL = srv.URL

	// ★ 空 message_id ⇒ 报错且不发请求（无卡可更新）。
	if err := c.UpdateApprovalMessage(context.Background(), "  ", "APPROVED"); err == nil {
		t.Fatalf("空 message_id 应报错")
	}
	// ★ 空 status ⇒ 报错且不发请求（实测缺 status ⇒ 60001 no Status error）。
	if err := c.UpdateApprovalMessage(context.Background(), "om_1", "  "); err == nil {
		t.Fatalf("空 status 应报错（不发必败请求）")
	}
	mu.Lock()
	n := updates
	mu.Unlock()
	if n != 0 {
		t.Fatalf("空入参不得发请求，实际 %d 次", n)
	}

	if err := c.UpdateApprovalMessage(context.Background(), "om_1", "APPROVED"); err != nil {
		t.Fatalf("正常更新不应报错: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if updates != 1 {
		t.Errorf("update 调用次数 = %d, 期望 1", updates)
	}
	// ★★ 断言实际发出的 body **恰为** {"message_id":…,"status":…}（实测契约，多键/缺键均错）。
	var body map[string]any
	if err := json.Unmarshal([]byte(rawBody), &body); err != nil {
		t.Fatalf("解析请求体失败: %v（body=%s）", err, rawBody)
	}
	if len(body) != 2 {
		t.Errorf("请求体键数 = %d, 期望恰 2（message_id+status）: %s", len(body), rawBody)
	}
	if body["message_id"] != "om_1" || body["status"] != "APPROVED" {
		t.Errorf("请求体 = %s, 期望 {\"message_id\":\"om_1\",\"status\":\"APPROVED\"}", rawBody)
	}
}

// N-060 F2（01a §5.5 约束3）：配额 ≥90% ⇒ 跳过 Bot 通知（降非关键、保对账）。
// throttle=true ⇒ Send 返回 nil 且**零次** message/send；throttle=false ⇒ 正常发送。
func TestNotifySenderQuotaThrottleSkipsBot(t *testing.T) {
	f := newFakeFeishuServer(t, 0, "success")
	db := storetest.NewDB(t)
	bizNo := seedNotifyInstance(t, db)
	sender := NewNotifySender(db, newTestHTTPClient(t, f), "https://jx.example.com", nil)
	sender.SetQuotaThrottle(func() bool { return true }) // 模拟水位 ≥90%

	if err := sender.Send(context.Background(), bizNo, "ou_target", "TRANSFERRED"); err != nil {
		t.Fatalf("降级跳过应返回 nil（策略性不发送 ≠ 失败）：%v", err)
	}
	f.mu.Lock()
	n := len(f.sendBodies)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("message/send 调用 = %d, 期望 0（≥90%% 降非关键 Bot）", n)
	}

	// 反向：门关闭 ⇒ 正常发送（证明确实是 throttle 在拦，不是环境问题）。
	sender.SetQuotaThrottle(func() bool { return false })
	if err := sender.Send(context.Background(), bizNo, "ou_target", "TRANSFERRED"); err != nil {
		t.Fatalf("门关闭后应正常发送：%v", err)
	}
	f.mu.Lock()
	n = len(f.sendBodies)
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("反向 message/send 调用 = %d, 期望 1", n)
	}
}
