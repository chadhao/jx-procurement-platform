package httpapi

import (
	"net/http"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 本文件覆盖「真实路由装配」——S2 并行交付时 M1/M6 用例自建最小 Echo，
// 不经过 router.go，因此「路由漏注册」这类缺陷只有本文件能抓到。
//
// 判别技巧：未注册的路径会被 SPA 兜底/404 处理，而注册在 /api 组内的路径
// 必先过 requireSession → 40100。故「未带会话请求返回 40100」即可证明路由已注册且已挂鉴权。

// expectRoutes 本次集成必须存在的路由集合（方法 + 路径）。
var expectRoutes = []string{
	// 看板（M5）
	"GET /api/dashboard/:id",
	"GET /api/dashboard/:id/export",
	// 备付金（M1）
	"GET /api/petty-cash/balance",
	"POST /api/petty-cash/receipt",
	"POST /api/petty-cash/monthly-close",
	// 报送（M6）
	"POST /api/submission",
	"GET /api/submission",
	"GET /api/submission/:id/package",
	"POST /api/submission/:id/receipt",
	"POST /api/submission/:id/group",
	"POST /api/submission/:id/reject",
	// 集团报销跟踪表（M1，FR-M1-02）
	"POST /api/reimbursement",
	"GET /api/reimbursement",
	"PATCH /api/reimbursement/:id",
	// 变更链回溯（M4，FR-M4-07）
	"GET /api/contract/:biz_no/changes",
	// 既有基线路由（防回归：改 router.go 时误删）
	"GET /api/me",
	"GET /api/instances",
	"GET /api/ledger/:table",
	"PATCH /api/ledger/:table/:id",
	"GET /api/audit/logs",
	"GET /api/admin/permission-rules",
	"PUT /api/admin/permission-rules",
	"POST /api/admin/users",
	"PATCH /api/admin/users/:open_id",
	"GET /healthz",
	"GET /readyz",
	"POST /internal/sync/subscribe",
	"POST /internal/sync/reconcile",
	"GET /auth/feishu/callback",
	"POST /auth/logout",
}

// TestRouterRegistersExpectedRoutes 断言真实路由表包含全部新增与基线路由，且无重复注册。
func TestRouterRegistersExpectedRoutes(t *testing.T) {
	e, _, _, _ := newAdminTestApp(t)

	registered := map[string]bool{}
	for _, r := range e.Routes() {
		key := r.Method + " " + r.Path
		if registered[key] {
			t.Errorf("路由重复注册: %s", key)
		}
		registered[key] = true
	}

	for _, want := range expectRoutes {
		if !registered[want] {
			t.Errorf("路由缺失: %s", want)
		}
	}
}

// TestRouterNewRoutesRequireSession 未带会话访问新增路由 → 40100（证明已注册且已挂会话鉴权）。
//
// 若路由漏注册，响应会是 404/40400 或 SPA 兜底 200，本用例即失败。
func TestRouterNewRoutesRequireSession(t *testing.T) {
	e, _, _, _ := newAdminTestApp(t)

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/dashboard/13"},
		{http.MethodGet, "/api/dashboard/13/export"},
		{http.MethodGet, "/api/petty-cash/balance"},
		{http.MethodPost, "/api/petty-cash/receipt"},
		{http.MethodPost, "/api/petty-cash/monthly-close"},
		{http.MethodPost, "/api/submission"},
		{http.MethodGet, "/api/submission"},
		{http.MethodGet, "/api/submission/1/package"},
		{http.MethodPost, "/api/submission/1/receipt"},
		{http.MethodPost, "/api/submission/1/group"},
		{http.MethodPost, "/api/submission/1/reject"},
		{http.MethodPost, "/api/reimbursement"},
		{http.MethodGet, "/api/reimbursement"},
		{http.MethodPatch, "/api/reimbursement/1"},
		{http.MethodGet, "/api/contract/CT-2609-0001/changes"},
	}
	for _, tc := range cases {
		rec, env := doRequest(e, tc.method, tc.path, "", "")
		if rec.Code != http.StatusUnauthorized || env.Code != codeUnauthorized {
			t.Errorf("%s %s: http=%d code=%d, 期望 401 / 40100（路由已注册且受会话保护）",
				tc.method, tc.path, rec.Code, env.Code)
		}
	}
}

// TestRouterPettyCashRoleGateViaRealRouter 真实路由下备付金角色口径：
// 综合运营主管 / 主管领导（只读）可访问余额；项目总经理 → 40300（前端菜单同口径）。
func TestRouterPettyCashRoleGateViaRealRouter(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true},
		store.UserRole{OpenID: "ou_lead", Role: roleDeptLead, Active: true},
		store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true},
	)

	allowed := []string{"ou_ops", "ou_lead"}
	for _, openID := range allowed {
		cookie := auth.Establish(openID)
		rec, env := doRequest(e, http.MethodGet, "/api/petty-cash/balance", cookie, "")
		if rec.Code != http.StatusOK || env.Code != codeOK {
			t.Errorf("角色 %s 读备付金余额: http=%d code=%d, 期望 200/0", openID, rec.Code, env.Code)
		}
	}

	gmCookie := auth.Establish("ou_gm")
	rec, env := doRequest(e, http.MethodGet, "/api/petty-cash/balance", gmCookie, "")
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Errorf("项目总经理读备付金余额: http=%d code=%d, 期望 403/40300（与前端菜单口径一致）",
			rec.Code, env.Code)
	}
}

// TestRouterSubmissionRoleGateViaRealRouter 真实路由下报送角色口径：
// 综合运营主管（读写）/ 项目总经理（只读列表）；申请人 → 40300。
func TestRouterSubmissionRoleGateViaRealRouter(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true},
		store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true},
		store.UserRole{OpenID: "ou_app", Role: "申请人", Active: true},
	)

	// 综合运营主管可写。
	opsCookie := auth.Establish("ou_ops")
	rec, env := doRequest(e, http.MethodPost, "/api/submission", opsCookie,
		`{"biz_no":"SUB-RT-1","subject_type":"公户付款"}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("综合运营主管提交报送: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}

	// 项目总经理可读列表。
	gmCookie := auth.Establish("ou_gm")
	rec, env = doRequest(e, http.MethodGet, "/api/submission", gmCookie, "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Errorf("项目总经理读报送列表: http=%d code=%d, 期望 200/0", rec.Code, env.Code)
	}

	// 项目总经理不可写（写仅限综合运营主管）。
	rec, env = doRequest(e, http.MethodPost, "/api/submission", gmCookie,
		`{"biz_no":"SUB-RT-2","subject_type":"公户付款"}`)
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Errorf("项目总经理提交报送: http=%d code=%d, 期望 403/40300（写权限仅归口综合运营主管）",
			rec.Code, env.Code)
	}

	// 申请人完全无权限。
	appCookie := auth.Establish("ou_app")
	rec, env = doRequest(e, http.MethodGet, "/api/submission", appCookie, "")
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Errorf("申请人读报送列表: http=%d code=%d, 期望 403/40300", rec.Code, env.Code)
	}
}
