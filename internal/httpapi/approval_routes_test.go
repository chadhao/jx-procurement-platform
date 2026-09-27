package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
)

// approval_routes_test.go —— 转向 ③ 审批路由的**装配级**断言（真实 router.go）。
//
// ★ 本文件的核心是 ②：证明入站回调 `POST /approval/external/callback` **不经过会话中间件**
//   （docs/11 R14 / docs/05 §3.14）——飞书回调请求**无 cookie**，若挂 `requireSession` 会
//   401 全失败且静默。判别法：与 `/api` 组对照——回调「不带会话 → **非 401**」而页面两键
//   「不带会话 → **必 401**」，二者同 Router 一次比出。

// TestExternalCallbackBypassesSessionMiddleware 回调不经过 requireSession；页面两键必须经过。
func TestExternalCallbackBypassesSessionMiddleware(t *testing.T) {
	e, _, _, _ := newAdminTestApp(t)

	// ① 回调路由：不带会话、带合法 JSON 体 → 绝不返回 401（本 fixture 未装配 Flow → 期望 503）。
	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "",
		`{"action_name":"APPROVE","biz_no":"PR-1","task_id":"t1","token":"x"}`)
	if rec.Code == http.StatusUnauthorized || env.Code == codeUnauthorized {
		t.Fatalf("回调路由不得经过会话中间件（R14）：http=%d code=%d", rec.Code, env.Code)
	}

	// ② 对照：同 Router 下 `/api` 组的审批两键 → 无会话必须 401/40100。
	rec2, env2 := doRequest(e, http.MethodPost, "/api/approval/PR-1/approve", "", `{}`)
	if rec2.Code != http.StatusUnauthorized || env2.Code != codeUnauthorized {
		t.Fatalf("页面两键应在会话保护下（对照点）：http=%d code=%d", rec2.Code, env2.Code)
	}
}

// TestApprovalRoutesRegistered 断言转向 ③ 的路由均已注册（回调在 root、其余在 /api 组）。
func TestApprovalRoutesRegistered(t *testing.T) {
	e, _, _, _ := newAdminTestApp(t)
	registered := map[string]bool{}
	for _, r := range e.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	want := []string{
		"POST /approval/external/callback", // ★ 独立入站面（root 组）
		"POST /api/approval/submit",        // ★ 我方提交
		"POST /api/approval/:biz_no/approve",
		"POST /api/approval/:biz_no/reject",
		"POST /api/approval/:biz_no/transfer",
		"POST /api/approval/:biz_no/addsign",
		"POST /api/approval/:biz_no/rollback",
		"POST /api/approval/:biz_no/cancel",
		"GET /api/approval/tasks",
		"GET /api/approval/defs",        // ★ 定义清单（管理员）
		"GET /api/approval/:biz_no",     // ★ 单实例详情 + 时间线
		"POST /internal/approval/check", // ★ 审批对账唯一入口（R24）
	}
	for _, w := range want {
		if !registered[w] {
			t.Errorf("路由缺失: %s", w)
		}
	}
}

// TestApprovalApiRoutesRequireSession 页面两键 / 四操作 / 待办在无会话时均为 40100。
func TestApprovalApiRoutesRequireSession(t *testing.T) {
	e, _, _, _ := newAdminTestApp(t)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/approval/PR-1/approve"},
		{http.MethodPost, "/api/approval/PR-1/reject"},
		{http.MethodPost, "/api/approval/PR-1/transfer"},
		{http.MethodPost, "/api/approval/PR-1/addsign"},
		{http.MethodPost, "/api/approval/PR-1/rollback"},
		{http.MethodPost, "/api/approval/PR-1/cancel"},
		{http.MethodGet, "/api/approval/tasks"},
	}
	for _, tc := range cases {
		rec, env := doRequest(e, tc.method, tc.path, "", "")
		if rec.Code != http.StatusUnauthorized || env.Code != codeUnauthorized {
			t.Errorf("%s %s: http=%d code=%d，期望 401/40100（已注册且受会话保护）",
				tc.method, tc.path, rec.Code, env.Code)
		}
	}
}

// TestCallbackWiring_InstanceCodeAndAssignee 装配级守卫（防"接线被删"这类静默回归）。
//
//	(a) 回调处理器**必须**把报文 `instance_code` 下传进 `flow.CallbackRequest.InstanceCode`
//	    —— 否则 flow 的「报文 instance_code 与 biz_no 不一致 → 拒绝」在生产**不可达**
//	    （flow 单测是**直接构造该字段**验证的，故接线正确性只能在此守护）。
//	(b) 回调错误块**必须**把 `flow.ErrNotAssignee` 映射（403），否则"非本人回调"落 500。
//
// ★ 这是**装配守卫**（读源码断言），非行为测试：行为语义由 `internal/flow` 的
//
//	`callback_test.go`（如 `TestCallbackInstanceCodeMismatchRejected`）端到端覆盖；
//	此处只防"接线被删/改名"。读源码的断言方式与 `cmd/jxapproval` 的装配守卫一致。
func TestCallbackWiring_InstanceCodeAndAssignee(t *testing.T) {
	src, err := os.ReadFile("handlers_approval.go")
	if err != nil {
		t.Fatalf("读取 handlers_approval.go 失败: %v", err)
	}
	s := string(src)
	// (a) 容忍 gofmt 对齐：InstanceCode:<空格>strings.TrimSpace(body.InstanceCode)
	if re := regexp.MustCompile(`InstanceCode:\s+strings\.TrimSpace\(body\.InstanceCode\)`); !re.MatchString(s) {
		t.Errorf("回调接线缺失：应把 body.InstanceCode 下传进 flow.CallbackRequest（否则防串单校验在生产不可达）")
	}
	// (b) 非本人回调 → 显式映射
	if !strings.Contains(s, "errors.Is(err, flow.ErrNotAssignee)") {
		t.Errorf("回调接线缺失：错误块应映射 flow.ErrNotAssignee（否则非本人回调落 500）")
	}
}

// TestCallbackErrorStatusEnumerated 回调错误→状态码**枚举式**覆盖（定案 #60：枚举优于逐例）。
//
// ★ 每行即一个枚举项——**任一项映射被改/删，对应行必红**。
// ★ 「已受理但推进失败」(#69 半, `res.Accepted==true` + err≠nil)**不在**本表（另由 #69 处理）。
func TestCallbackErrorStatusEnumerated(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"ErrInvalidToken→403", flow.ErrInvalidToken, http.StatusForbidden, codeForbidden},
		{"ErrNotAssignee→403", flow.ErrNotAssignee, http.StatusForbidden, codeRowForbidden},
		{"ErrInvalidSubmit→400", flow.ErrInvalidSubmit, http.StatusBadRequest, codeBadRequest},
		{"ErrIllegalTransition→409", flow.ErrIllegalTransition, http.StatusConflict, codeApprovalConflict},
		{"ErrTaskHeld→409", flow.ErrTaskHeld, http.StatusConflict, codeApprovalConflict},
		{"ErrNodeNotReached→409", flow.ErrNodeNotReached, http.StatusConflict, codeApprovalConflict},
		{"ErrDefinitionMissing→409", flow.ErrDefinitionMissing, http.StatusConflict, codeApprovalConflict},
		// 包装后的哨兵仍须命中（flow 侧以 fmt.Errorf("%w: …") 包装后返回）。
		{"wrapped ErrInvalidSubmit→400", fmt.Errorf("%w: 现场包装", flow.ErrInvalidSubmit), http.StatusBadRequest, codeBadRequest},
		{"wrapped ErrNotAssignee→403", fmt.Errorf("%w: 现场包装", flow.ErrNotAssignee), http.StatusForbidden, codeRowForbidden},
		// 未分类 → 5xx（服务端故障），绝不静默落 200。
		{"未分类→500", errors.New("some server fault"), http.StatusInternalServerError, codeInternal},
	}
	for _, tc := range cases {
		gotStatus, gotCode := callbackErrorStatus(tc.err)
		if gotStatus != tc.wantStatus || gotCode != tc.wantCode {
			t.Errorf("%s: 得 (status=%d, code=%d)，期望 (status=%d, code=%d)",
				tc.name, gotStatus, gotCode, tc.wantStatus, tc.wantCode)
		}
	}
}
