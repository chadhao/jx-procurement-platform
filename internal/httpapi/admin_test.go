package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	jsync "github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

// newAdminTestApp 装配开发模式路由，并额外返回鉴权器与权限装载器（供系统管理用例使用）。
func newAdminTestApp(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator, *permission.Loader) {
	t.Helper()
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	maps := &config.Maps{}

	client := &feishu.FakeClient{}
	inboxSvc := inbox.NewService(db, metrics, nil)
	ingestor := worker.NewIngestor(db, maps, nil)
	wk := worker.NewWorker(db, client, ingestor, metrics, nil)
	sub := jsync.NewSubscriber(db, client, maps, metrics, nil)
	rec := jsync.NewReconciler(db, client, ingestor, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub, Reconciler: rec,
		Perm: perm, Auth: auth, Maps: maps, WebUI: nil, Version: "test",
	})
	return e, db, auth, perm
}

// doRequest 发送一次请求（可带会话 Cookie 与 JSON 请求体），返回响应与解析后的 Envelope。
func doRequest(e *echo.Echo, method, path, cookie, body string) (*httptest.ResponseRecorder, Envelope) {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: access.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

// mustData 将 Envelope.Data 解析为 map。
func mustData(t *testing.T, env Envelope) map[string]any {
	t.Helper()
	if m, ok := env.Data.(map[string]any); ok {
		return m
	}
	raw, _ := json.Marshal(env.Data)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 data 失败: %v (data=%s)", err, string(raw))
	}
	return m
}

func seedDefaultUsers(t *testing.T, db *store.DB, rows ...store.UserRole) {
	t.Helper()
	for _, r := range rows {
		if err := db.UpsertUserRole(context.Background(), r); err != nil {
			t.Fatalf("写入用户角色失败: %v", err)
		}
	}
}

func seedInstance(t *testing.T, db *store.DB, code, applicant, dept string) {
	t.Helper()
	now := time.Now().UTC()
	// ★ 单据号按实例唯一：架构转向 ③ 起 t_instance.biz_no 有 UNIQUE 兜底（migration 0007），
	//   两个不同实例共用同一单号＝「同号两笔」，本就非法（原 fixture 用固定号属历史遗留）。
	if err := db.UpsertInstance(context.Background(), &store.Instance{
		InstanceCode: code, ApprovalCode: "ac-todo-q1", DocType: "PR", BizNo: "PR-2609-" + code,
		Status: "APPROVED", StatusRaw: "APPROVED", ApplicantOpenID: applicant,
		Department: dept, Source: "event", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入实例失败: %v", err)
	}
}

// TestAdminPermissionRuleChangeImmediate 改规则后下一请求即生效（不重启、不改代码，TC-33）。
func TestAdminPermissionRuleChangeImmediate(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true},
		store.UserRole{OpenID: "ou_me", Role: "申请人", Department: "生产部", Active: true},
	)
	seedInstance(t, db, "I-ME", "ou_me", "生产部")
	seedInstance(t, db, "I-OTHER", "ou_other", "销售部")

	meCookie := auth.Establish("ou_me")
	adminCookie := auth.Establish("ou_admin")

	// ① 首次访问：申请人 SELF → 仅本人 1 行（同时让装载器缓存 SELF 决策）。
	rec, env := doRequest(e, http.MethodGet, "/api/instances", meCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("首次列表状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	first := mustData(t, env)
	if got := first["total"].(float64); got != 1 {
		t.Fatalf("SELF 可见行数 = %v, 期望 1", got)
	}

	// ② 管理员读取矩阵并枚举项。
	_, mEnv := doRequest(e, http.MethodGet, "/api/admin/permission-rules", adminCookie, "")
	matrix := mustData(t, mEnv)
	if _, ok := matrix["resources"]; !ok {
		t.Fatal("矩阵响应缺少 resources")
	}
	scopes, _ := matrix["row_scopes"].([]any)
	if len(scopes) != 7 {
		t.Fatalf("row_scopes 数量 = %d, 期望 7（6 令牌 + DENY）", len(scopes))
	}
	items, _ := matrix["items"].([]any)
	if len(items) == 0 {
		t.Fatal("矩阵 items 为空")
	}

	// ③ 仅把「申请人 × api:instances」改为 ALL，整表回写。
	rules := make([]map[string]any, 0, len(items))
	changed := false
	for _, it := range items {
		m := it.(map[string]any)
		if m["resource"] == "api:instances" && m["role"] == "申请人" {
			m["row_scope"] = "ALL"
			changed = true
		}
		rules = append(rules, m)
	}
	if !changed {
		t.Fatal("未找到 申请人 × api:instances 规则（默认口径缺失）")
	}
	putBody, _ := json.Marshal(map[string]any{"rules": rules})
	prec, penv := doRequest(e, http.MethodPut, "/api/admin/permission-rules", adminCookie, string(putBody))
	if prec.Code != http.StatusOK || penv.Code != 0 {
		t.Fatalf("保存矩阵失败: http=%d body=%s", prec.Code, prec.Body.String())
	}
	saved := mustData(t, penv)
	if saved["effective"] != "immediate" {
		t.Errorf("effective = %v, 期望 immediate", saved["effective"])
	}

	// ④ 不重启，同一会话再次访问 → 立即扩大为 2 行。
	_, env2 := doRequest(e, http.MethodGet, "/api/instances", meCookie, "")
	second := mustData(t, env2)
	if got := second["total"].(float64); got != 2 {
		t.Fatalf("改规则后可见行数 = %v, 期望 2（缓存未清或规则未生效）", got)
	}
}

// TestAdminUserDeactivateImmediate 停用人员后下一请求即被拒（TC-34）。
func TestAdminUserDeactivateImmediate(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true},
		store.UserRole{OpenID: "ou_b", Role: "主管领导", Department: "生产部", Active: true},
	)
	adminCookie := auth.Establish("ou_admin")
	bCookie := auth.Establish("ou_b")

	// ① B 首次访问成功。
	rec, _ := doRequest(e, http.MethodGet, "/api/instances", bCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("B 首次访问状态码 = %d, 期望 200", rec.Code)
	}

	// ② 管理员停用 B。
	prec, penv := doRequest(e, http.MethodPatch, "/api/admin/users/ou_b", adminCookie, `{"active":false}`)
	if prec.Code != http.StatusOK {
		t.Fatalf("停用 B 失败: http=%d body=%s", prec.Code, prec.Body.String())
	}
	if u := mustData(t, penv); u["active"] != false {
		t.Errorf("停用后 active = %v, 期望 false", u["active"])
	}

	// ③ B 不重新登录，下一请求即被拒（40101 未映射角色）。
	rec2, env2 := doRequest(e, http.MethodGet, "/api/instances", bCookie, "")
	if rec2.Code != http.StatusUnauthorized || env2.Code != codeRoleMapped {
		t.Fatalf("停用后状态码 = %d / code=%d, 期望 401 / 40101", rec2.Code, env2.Code)
	}
}

// TestAdminPermissionChangeAuditDiff 权限变更全量留痕（改前/改后 diff，TC-35）。
func TestAdminPermissionChangeAuditDiff(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true},
		store.UserRole{OpenID: "ou_c", Role: "验收人", Active: true},
	)
	adminCookie := auth.Establish("ou_admin")

	// 变更一：改规则表（验收人 × ledger:* → ALL）。
	_, mEnv := doRequest(e, http.MethodGet, "/api/admin/permission-rules", adminCookie, "")
	items := mustData(t, mEnv)["items"].([]any)
	rules := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m := it.(map[string]any)
		if m["resource"] == "ledger:*" && m["role"] == "验收人" {
			m["row_scope"] = "ALL"
		}
		rules = append(rules, m)
	}
	body, _ := json.Marshal(map[string]any{"rules": rules})
	if rec, _ := doRequest(e, http.MethodPut, "/api/admin/permission-rules", adminCookie, string(body)); rec.Code != http.StatusOK {
		t.Fatalf("改规则表失败: %s", rec.Body.String())
	}

	// 变更二：改人员角色（C 改为 主管领导）。
	if rec, _ := doRequest(e, http.MethodPatch, "/api/admin/users/ou_c", adminCookie, `{"role":"主管领导","department":"生产部"}`); rec.Code != http.StatusOK {
		t.Fatalf("改人员角色失败: %s", rec.Body.String())
	}

	// 查询审计：action=permission_update，每条须含 before/after diff。
	rec, env := doRequest(e, http.MethodGet, "/api/audit/logs?action=permission_update", adminCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("查审计日志失败: http=%d body=%s", rec.Code, rec.Body.String())
	}
	logs := mustData(t, env)["items"].([]any)
	if len(logs) < 2 {
		t.Fatalf("permission_update 审计条数 = %d, 期望 ≥2", len(logs))
	}
	for i, l := range logs {
		m := l.(map[string]any)
		if m["actor_open_id"] != "ou_admin" || m["actor_role"] != roleSysAdmin {
			t.Errorf("审计[%d] 操作者缺失: %+v", i, m)
		}
		detail, _ := m["detail_json"].(string)
		if !strings.Contains(detail, "before") || !strings.Contains(detail, "after") {
			t.Errorf("审计[%d] detail_json 缺改前/改后: %s", i, detail)
		}
	}
}

// TestAdminNonAdminForbidden 非系统管理员调用系统管理接口 → 40300 并留痕。
func TestAdminNonAdminForbidden(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_lead", Role: "主管领导", Active: true})
	cookie := auth.Establish("ou_lead")

	rec, env := doRequest(e, http.MethodGet, "/api/admin/permission-rules", cookie, "")
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Fatalf("非管理员调用状态码 = %d / code=%d, 期望 403 / 40300", rec.Code, env.Code)
	}
	// 留痕：deny 记录存在。
	n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_audit_log WHERE action='denied' AND result='deny'`)
	if n < 1 {
		t.Errorf("非管理员越权调用未留痕（denied 记录数=%d）", n)
	}
	// 非法 row_scope（条件表达式）必须被拒且不改表。
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true})
	adminCookie := auth.Establish("ou_admin")
	badRec, badEnv := doRequest(e, http.MethodPut, "/api/admin/permission-rules", adminCookie,
		`{"rules":[{"resource":"ledger:*","role":"申请人","row_scope":"department = 'x'","column_deny":[]}]}`)
	if badRec.Code != http.StatusBadRequest || badEnv.Code != codeBadRequest {
		t.Fatalf("非法 row_scope 未返回 40000: http=%d code=%d", badRec.Code, badEnv.Code)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_permission_rule WHERE row_scope LIKE '%department%'`); n != 0 {
		t.Errorf("非法条件表达式被写入规则表（%d 行）", n)
	}
}

// TestAdminSeedIdempotent 播种可重复执行且不覆盖已改规则。
func TestAdminSeedIdempotent(t *testing.T) {
	_, db, _, _ := newAdminTestApp(t)
	ctx := context.Background()

	n1, err := seed.SeedQ3Defaults(ctx, db)
	if err != nil {
		t.Fatalf("首次播种失败: %v", err)
	}
	if n1 != len(seed.Roles)*len(seed.Resources) {
		t.Fatalf("首次播种行数 = %d, 期望 %d", n1, len(seed.Roles)*len(seed.Resources))
	}
	// 管理员改动一行后重跑：不得被覆盖。
	if err := db.UpsertPermissionRule(ctx, store.PermissionRule{
		Resource: "ledger:*", Role: "申请人", RowScope: "ALL",
	}); err != nil {
		t.Fatalf("写入规则失败: %v", err)
	}
	if n2, _ := seed.SeedQ3Defaults(ctx, db); n2 != 0 {
		t.Errorf("重复播种新增行数 = %d, 期望 0", n2)
	}
	rule, err := db.GetPermissionRule(ctx, "ledger:*", "申请人")
	if err != nil {
		t.Fatalf("读取规则失败: %v", err)
	}
	if rule.RowScope != "ALL" {
		t.Errorf("播种覆盖了管理员改动: row_scope=%q, 期望 ALL", rule.RowScope)
	}
}
