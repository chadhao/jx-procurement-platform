package httpapi

import (
	"net/http"
	"testing"
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
		"POST /api/approval/:biz_no/approve",
		"POST /api/approval/:biz_no/reject",
		"POST /api/approval/:biz_no/transfer",
		"POST /api/approval/:biz_no/addsign",
		"POST /api/approval/:biz_no/rollback",
		"POST /api/approval/:biz_no/cancel",
		"GET /api/approval/tasks",
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
