package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"

	"github.com/labstack/echo/v4"
)

// callback_repair_test.go —— 回调链路修复第 2 批（docs/16 §2-A/§2-B/§2-E）的行为测试。
//
// 覆盖：① 官方字段报文（经 action_context 解 biz_no / user_id 转换 open_id）端到端 200；
// ② 旧自造报文（顶层 biz_no / 纯字符串 action_context）兼容读；
// ③ user_id 转换失败 ⇒ 40000 可见拒绝、不占幂等键；
// ④ callbackBodyLog 脱敏（token 不落明文）与 64KB 截断。

// newRepairRouter 回调修复测试专用装配：与 newCallbackProbeApp（callback_contract_test.go）
// 同一形态，另带 Contact 端口与日志捕获（logOut 非 nil 时同步落一份）。
func newRepairRouter(t *testing.T, db *store.DB, svc *flow.Service, contact feishu.ContactClient, logOut *strings.Builder) *echo.Echo {
	t.Helper()
	logger := observ.NewLogger("info", nil)
	if logOut != nil {
		logger = observ.NewLogger("info", logOut)
	}
	return NewRouter(Deps{
		Env:     &config.Env{DevMode: true},
		DB:      db,
		Log:     logger,
		Metrics: observ.NewMetrics(),
		Health:  observ.NewHealth("test"),
		Flow:    svc,
		Contact: contact,
		Auth:    access.NewAuthenticator(db, access.NewStore(db, "probe-key", time.Hour), nil, true, nil),
		Maps:    &config.Maps{},
	})
}

// officialCallbackBody 构造一份**官方《三方快捷审批回调》字段口径**的报文
// （docs/05-API §3.14 / docs/16 §2-A-1）：官方不发顶层 biz_no / open_id。
func officialCallbackBody(bizNo, taskID, userID, approvalCode, token, messageID string) string {
	return `{
		"action_type": "APPROVE",
		"user_id": "` + userID + `",
		"approval_code": "` + approvalCode + `",
		"token": "` + token + `",
		"action_context": "{\"biz_no\":\"` + bizNo + `\",\"task_id\":\"` + taskID + `\"}",
		"instance_id": "app:` + bizNo + `",
		"task_id": "` + taskID + `",
		"message_id": "` + messageID + `",
		"id": "om_123",
		"reason": "同意",
		"attachments": [{"file_id":"f1"}]
	}`
}

// TestCallbackOfficialFieldsResolvesAndRecords 官方字段报文端到端：
// biz_no 经 action_context 解出、task_id 顶层命中、user_id 转换为 open_id 后准入、
// message_id 落 t_flow_op_log.message_id（0013 新列，R26 写入者实证）。
func TestCallbackOfficialFieldsResolvesAndRecords(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	contact := &feishu.FakeContactClient{Mappings: map[string]string{"u_m1": "ou_m1"}}
	e := newRepairRouter(t, db, svc, contact, nil)

	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "",
		officialCallbackBody(bizNo, taskID, "u_m1", "code-pr", "tok-pr", "om_card_001"))
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("官方字段报文应 200，实际 http=%d body=%s", rec.Code, rec.Body.String())
	}

	ops, err := db.ListFlowOpLogs(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读 op_log 失败: %v", err)
	}
	var approved []store.FlowOpLog
	for _, o := range ops {
		if o.OpType == flow.OpApprove {
			approved = append(approved, o)
		}
	}
	if len(approved) != 1 {
		t.Fatalf("APPROVE op_log 应恰 1 条: n=%d", len(approved))
	}
	if approved[0].ActorOpenID != "ou_m1" {
		t.Errorf("操作人应经 user_id→open_id 转换为 ou_m1，实际 %q", approved[0].ActorOpenID)
	}
	if approved[0].MessageID != "om_card_001" {
		t.Errorf("message_id 应落盘（0013 新列写入者），实际 %q", approved[0].MessageID)
	}
	if contact.CallCount() != 1 {
		t.Errorf("转换端口应恰好调用 1 次，实际 %d", contact.CallCount())
	}
}

// TestCallbackOldFormatCompat 旧自造报文（顶层 biz_no / action_name / 纯字符串
// action_context / open_id）在兼容窗口内仍可解析（docs/16 §2-A-2）。
func TestCallbackOldFormatCompat(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	// 旧报文走 open_id（同域免转换），Contact 显式置 nil 验证不触发转换路径。
	e := newRepairRouter(t, db, svc, nil, nil)
	body := `{"action_name":"APPROVE","biz_no":"` + bizNo + `","task_id":"` + taskID +
		`","token":"tok-pr","action_context":"` + taskID + `","operator":{"open_id":"ou_m1"}}`
	rec, _ := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("旧自造报文（兼容窗口内）应仍 200，实际 http=%d body=%s", rec.Code, rec.Body.String())
	}
	ops, _ := db.ListFlowOpLogs(context.Background(), bizNo)
	var approved []store.FlowOpLog
	for _, o := range ops {
		if o.OpType == flow.OpApprove {
			approved = append(approved, o)
		}
	}
	if len(approved) != 1 || approved[0].ActorOpenID != "ou_m1" {
		t.Fatalf("旧报文操作人应直读 open_id，实际 %+v", approved)
	}
}

// TestCallbackUserIDConversionFailureVisibleReject user_id 转换失败 ⇒
// **可见拒绝 40000 ＋ 不占幂等键**（docs/16 §2-A-3 红线；定案 #62：准入失败不消耗幂等键）。
func TestCallbackUserIDConversionFailureVisibleReject(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	contact := &feishu.FakeContactClient{ResolveFn: func(ctx context.Context, userID string) (string, error) {
		return "", errors.New("通讯录查无此人（模拟）")
	}}
	e := newRepairRouter(t, db, svc, contact, nil)

	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "",
		officialCallbackBody(bizNo, taskID, "u_ghost", "code-pr", "tok-pr", "om_x"))
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Fatalf("user_id 转换失败应可见拒绝 400/40000，实际 http=%d body=%s", rec.Code, rec.Body.String())
	}
	// 不落 op_log（更不占幂等键）：其后同键真实回调不得被判 Duplicate。
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_flow_op_log WHERE biz_no=? AND task_id=?`, bizNo, taskID); n != 0 {
		t.Fatalf("转换失败的回调不得落 op_log / 占幂等键，实际 %d 条", n)
	}
	// 同键、身份正确的回调仍可正常受理（幂等键未被污染）。
	e2 := newRepairRouter(t, db, svc,
		&feishu.FakeContactClient{Mappings: map[string]string{"u_m1": "ou_m1"}}, nil)
	rec2, _ := doRequest(e2, http.MethodPost, "/approval/external/callback", "",
		officialCallbackBody(bizNo, taskID, "u_m1", "code-pr", "tok-pr", "om_y"))
	if rec2.Code != http.StatusOK {
		t.Fatalf("转换失败未占键 ⇒ 同键真实回调应 200，实际 http=%d body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestCallbackBodyLogMasksToken callbackBodyLog 脱敏断言：日志**不出现 token 明文**，
// 记为 `token=<len:N>`；biz_no / message_id 可检索（docs/16 §2-E）。
func TestCallbackBodyLogMasksToken(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	var buf strings.Builder
	e := newRepairRouter(t, db, svc,
		&feishu.FakeContactClient{Mappings: map[string]string{"u_m1": "ou_m1"}}, &buf)
	rec, _ := doRequest(e, http.MethodPost, "/approval/external/callback", "",
		officialCallbackBody(bizNo, taskID, "u_m1", "code-pr", "tok-pr", "om_log"))
	if rec.Code != http.StatusOK {
		t.Fatalf("前置：回调应 200，实际 http=%d body=%s", rec.Code, rec.Body.String())
	}
	logs := buf.String()
	if strings.Contains(logs, "tok-pr") {
		t.Errorf("★ 留痕泄漏：日志出现 token 明文（应全打码）")
	}
	// ★ 日志为 JSON 形态，`<` 会被转义为 \u003c —— 断言打码形态的长度指纹 `len:6`。
	if !strings.Contains(logs, "len:6") {
		t.Errorf("token 应记为 token=<len:N>（len=6），日志=%s", logs)
	}
	if !strings.Contains(logs, "om_log") || !strings.Contains(logs, bizNo) {
		t.Errorf("留痕应含 message_id 与 biz_no（按单检索入口），日志=%s", logs)
	}
}

// TestCallbackBodyLogTruncatesOver64KB 留痕视图超 64KB ⇒ 截断并标 [truncated]
// （截断作用于日志视图，下游仍按完整 body 正常处理）。
func TestCallbackBodyLogTruncatesOver64KB(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	var buf strings.Builder
	e := newRepairRouter(t, db, svc,
		&feishu.FakeContactClient{Mappings: map[string]string{"u_m1": "ou_m1"}}, &buf)
	// 用 action_context（留痕原样落）撑爆 64KB 视图：4 万个汉字 ≈ 120KB UTF-8。
	bigAC := strings.Repeat("往", 40000)
	body := `{"action_type":"APPROVE","user_id":"u_m1","approval_code":"code-pr","token":"tok-pr",` +
		`"action_context":"{\"biz_no\":\"` + bizNo + `\",\"task_id\":\"` + taskID + `\",\"note\":\"` + bigAC + `\"}",` +
		`"instance_id":"app:` + bizNo + `","task_id":"` + taskID + `","reason":"同意"}`
	rec, _ := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("超长 action_context 不应影响回调处理（截断只作用于日志视图），实际 http=%d body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(buf.String(), "[truncated]") {
		t.Errorf("留痕视图超 64KB 应标 [truncated]")
	}
}

// TestCallbackConversionPortNotWiredIs503 转换端口未装配 ⇒ 503 可见失败（绝不静默放行）。
func TestCallbackConversionPortNotWiredIs503(t *testing.T) {
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	e := newRepairRouter(t, db, svc, nil, nil) // 未装配 Contact
	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "",
		officialCallbackBody(bizNo, taskID, "u_m1", "code-pr", "tok-pr", "om_z"))
	if rec.Code != http.StatusServiceUnavailable || env.Code != codeNotReady {
		t.Fatalf("转换端口未装配应 503 可见失败，实际 http=%d body=%s", rec.Code, rec.Body.String())
	}
}
