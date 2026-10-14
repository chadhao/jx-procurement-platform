package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/orgsync"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	fsync "github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

// admin_org_sync_test.go —— `POST /api/admin/org/sync`（人员管理页手动同步飞书通讯录）。
//
// 覆盖（任务验收判据）：
//   ① 管理员会话 ⇒ 200 且返回含计数字段，且镜像**确实落库**（同步真跑了）；
//   ② 非系统管理员 ⇒ 403/40300（负向），且镜像**未落库**（守卫拦在同步之前）；
//   ③ Runner 未装配（nil）⇒ 503（可见失败，不静默）。

// stubOrgFetcher orgsync.Fetcher 轻量替身：直接返回内存集合，不打网络、不依赖真机。
type stubOrgFetcher struct {
	depts       []*store.OrgDepartment
	usersByDept map[string][]*store.OrgUser
}

// ListDepartments 返回预置部门（触发失败由 failOnDepts 控制）。
func (s stubOrgFetcher) ListDepartments(_ context.Context) ([]*store.OrgDepartment, error) {
	return s.depts, nil
}

// ListUsersByDepartment 返回该部门预置用户。
func (s stubOrgFetcher) ListUsersByDepartment(_ context.Context, openDepartmentID string) ([]*store.OrgUser, error) {
	return s.usersByDept[openDepartmentID], nil
}

// failingOrgFetcher 拉取即失败（用于验证「同步失败 ⇒ 可见错误、不落库」的降级路径）。
type failingOrgFetcher struct{}

func (failingOrgFetcher) ListDepartments(_ context.Context) ([]*store.OrgDepartment, error) {
	return nil, errors.New("模拟拉取部门失败")
}

func (failingOrgFetcher) ListUsersByDepartment(_ context.Context, _ string) ([]*store.OrgUser, error) {
	return nil, errors.New("模拟拉取用户失败")
}

// newAdminOrgSyncApp 装配开发模式路由，并注入通讯录同步运行器（fetcher 为 nil 时 OrgSync 保持 nil）。
func newAdminOrgSyncApp(t *testing.T, f orgsync.Fetcher) (*echo.Echo, *store.DB, *access.Authenticator) {
	t.Helper()
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	maps := &config.Maps{}

	client := &feishu.FakeClient{}
	inboxSvc := inbox.NewService(db, metrics, nil)
	ingestor := worker.NewIngestor(db, maps, nil)
	wk := worker.NewWorker(db, client, ingestor, metrics, nil)
	sub := fsync.NewSubscriber(db, client, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore(db, "test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)

	var orgRunner *orgsync.Runner
	if f != nil {
		orgRunner = orgsync.NewRunner(f, db, metrics, observ.NewHealth("test"),
			observ.NewLogger("error", io.Discard), time.Hour)
	}

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub,
		Perm: perm, Auth: auth, Maps: maps, WebUI: nil, Version: "test",
		OrgSync: orgRunner,
		Spec:    metaTestBundle(t),
	})
	return e, db, auth
}

// sampleOrgFetcher 两个部门 + 两名人员的固定替身（用于断言计数）。
func sampleOrgFetcher() stubOrgFetcher {
	return stubOrgFetcher{
		depts: []*store.OrgDepartment{
			{OpenDepartmentID: "od-prod", DepartmentID: "od-prod", ParentOpenDepartmentID: "0", Name: "生产部"},
			{OpenDepartmentID: "od-hx", DepartmentID: "od-hx", ParentOpenDepartmentID: "0", Name: "江熙新材"},
		},
		usersByDept: map[string][]*store.OrgUser{
			"od-prod": {{OpenID: "ou_li", Name: "李四", PrimaryDepartmentID: "od-prod"}},
			"od-hx":   {{OpenID: "ou_hao", Name: "郝端", PrimaryDepartmentID: "od-hx"}},
		},
	}
}

// orgMirrorCounts 返回镜像在用行数（部门 / 人员）。
func orgMirrorCounts(t *testing.T, db *store.DB) (int, int) {
	t.Helper()
	depts, err := db.ListOrgDepartments(context.Background())
	if err != nil {
		t.Fatalf("读部门镜像失败: %v", err)
	}
	users, err := db.ListOrgUsers(context.Background())
	if err != nil {
		t.Fatalf("读人员镜像失败: %v", err)
	}
	return len(depts), len(users)
}

// TestAdminOrgSync_AdminOK ① 管理员会话触发 ⇒ 200 + 计数字段齐全，且镜像确实落库。
func TestAdminOrgSync_AdminOK(t *testing.T) {
	e, db, auth := newAdminOrgSyncApp(t, sampleOrgFetcher())
	seedSysAdmin(t, db, "ou_admin")
	cookie := auth.Establish("ou_admin")

	rec, env := doRequest(e, http.MethodPost, "/api/admin/org/sync", cookie, "")
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("管理员同步 http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	// 返回结构与内部端点 handleOrgSync 同构：计数字段一个不缺。
	for _, k := range []string{
		"trigger", "dept_total", "user_total", "dept_added", "dept_updated", "dept_soft_deleted",
		"user_added", "user_updated", "user_soft_deleted", "duration_ms",
	} {
		if _, ok := data[k]; !ok {
			t.Errorf("返回缺字段 %q: %v", k, data)
		}
	}
	if data["dept_total"].(float64) != 2 || data["user_total"].(float64) != 2 {
		t.Errorf("计数不符: dept_total=%v user_total=%v, 期望 2/2", data["dept_total"], data["user_total"])
	}
	if data["dept_added"].(float64) != 2 || data["user_added"].(float64) != 2 {
		t.Errorf("首次应全为新增: dept_added=%v user_added=%v", data["dept_added"], data["user_added"])
	}

	// 同步确实落了镜像（不是空转返回）。
	if dn, un := orgMirrorCounts(t, db); dn != 2 || un != 2 {
		t.Errorf("镜像落库行数 dept=%d user=%d, 期望 2/2", dn, un)
	}
	// 审计留痕：org_sync，操作者为管理员，来源标注 admin。
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_audit_log WHERE action='org_sync' AND actor_open_id='ou_admin'`); n < 1 {
		t.Errorf("管理端同步未写审计留痕（org_sync 记录数=%d）", n)
	}
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_audit_log WHERE action='org_sync' AND detail_json LIKE '%"source":"admin"%'`); n < 1 {
		t.Errorf("审计未标注来源为 admin（记录数=%d）", n)
	}
}

// TestAdminOrgSync_NonAdminForbidden ② 非系统管理员 ⇒ 403/40300，且**同步未执行**（镜像空）。
func TestAdminOrgSync_NonAdminForbidden(t *testing.T) {
	e, db, auth := newAdminOrgSyncApp(t, sampleOrgFetcher())
	// 仅审批角色、非系统管理员。
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_lead", Role: "主管领导", Department: "生产部", Active: true})
	cookie := auth.Establish("ou_lead")

	rec, env := doRequest(e, http.MethodPost, "/api/admin/org/sync", cookie, "")
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Fatalf("非管理员同步 http=%d code=%d, 期望 403/40300 body=%s", rec.Code, env.Code, rec.Body.String())
	}
	// ★ 守卫必须拦在同步之前：镜像应为空（否则「越权者触发了全量同步」）。
	if dn, un := orgMirrorCounts(t, db); dn != 0 || un != 0 {
		t.Errorf("越权调用却执行了同步（镜像 dept=%d user=%d，期望 0/0）", dn, un)
	}
	// 越权留痕（requireSysAdmin 内）。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_audit_log WHERE action='denied' AND result='deny'`); n < 1 {
		t.Errorf("越权调用未留痕（denied 记录数=%d）", n)
	}
}

// TestAdminOrgSync_Unauthenticated ③ 无会话 ⇒ 401/40100（会话域前置）。
func TestAdminOrgSync_Unauthenticated(t *testing.T) {
	e, _, _ := newAdminOrgSyncApp(t, sampleOrgFetcher())
	rec, env := doRequest(e, http.MethodPost, "/api/admin/org/sync", "", "")
	if rec.Code != http.StatusUnauthorized || env.Code != codeUnauthorized {
		t.Fatalf("未登录同步 http=%d code=%d, 期望 401/40100", rec.Code, env.Code)
	}
}

// TestAdminOrgSync_RunnerNil503 ④ Runner 未装配 ⇒ 503（可见失败，不静默）。
func TestAdminOrgSync_RunnerNil503(t *testing.T) {
	e, db, auth := newAdminOrgSyncApp(t, nil) // OrgSync 保持 nil
	seedSysAdmin(t, db, "ou_admin")
	cookie := auth.Establish("ou_admin")
	rec, env := doRequest(e, http.MethodPost, "/api/admin/org/sync", cookie, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Runner 为 nil 应 503，实得 http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
}

// TestAdminOrgSync_FetchFailureVisible ⑤ 同步失败 ⇒ 4xx/5xx 可见错误（不静默成功、不落库）。
func TestAdminOrgSync_FetchFailureVisible(t *testing.T) {
	e, db, auth := newAdminOrgSyncApp(t, failingOrgFetcher{})
	seedSysAdmin(t, db, "ou_admin")
	cookie := auth.Establish("ou_admin")
	rec, env := doRequest(e, http.MethodPost, "/api/admin/org/sync", cookie, "")
	if rec.Code == http.StatusOK || env.Code == 0 {
		t.Fatalf("拉取失败却返回成功: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	if dn, un := orgMirrorCounts(t, db); dn != 0 || un != 0 {
		t.Errorf("拉取失败却落了镜像（dept=%d user=%d，期望 0/0）", dn, un)
	}
}
