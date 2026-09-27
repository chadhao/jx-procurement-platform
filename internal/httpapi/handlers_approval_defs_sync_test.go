package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/approval"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	jsync "github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

// handlers_approval_defs_sync_test.go —— `POST /api/admin/approval/defs/sync`（docs/16 §2-C 通道①）单测。
//
// 覆盖：① 未授权（未登录 401 / 非系统管理员 403）⇒ 拒绝；
// ② 清单为空 / 占位符未替换 ⇒ 可见错误（400，绝不 200 静默）；
// ③ 回调配置缺失 ⇒ 可见错误（503）；④ 正常装载 ⇒ 计数可核对 + 幂等 + token/URL 落同值。

const (
	defsSyncDomain = "https://callback.example.com"
	defsSyncToken  = "action-callback-token-test"
)

// newDefsSyncTestApp 装配含 ApprovalDefs 的开发模式路由（复用 admin_test.go 的构造惯例）。
func newDefsSyncTestApp(t *testing.T, withCallbackConfig bool) (*echo.Echo, *store.DB, *access.Authenticator, *feishu.FakeExternalApprovalClient) {
	t.Helper()
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	if withCallbackConfig {
		env.CallbackDomain = defsSyncDomain
		env.ActionCallbackToken = defsSyncToken
	}
	maps := &config.Maps{}

	client := &feishu.FakeClient{}
	inboxSvc := inbox.NewService(db, metrics, nil)
	ingestor := worker.NewIngestor(db, maps, nil)
	wk := worker.NewWorker(db, client, ingestor, metrics, nil)
	sub := jsync.NewSubscriber(db, client, maps, metrics, nil)
	rec := jsync.NewReconciler(db, client, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)
	fakeExt := feishu.NewFakeExternalApprovalClient()

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub, Reconciler: rec,
		ApprovalDefs: approval.NewRegistry(db, fakeExt, nil),
		Perm:         perm, Auth: auth, Maps: maps, WebUI: nil, Version: "test",
	})
	return e, db, auth, fakeExt
}

// seedApprovalCodeRow 写入一条 approval_code 配置映射（map_key＝code、map_value＝doc_type）。
func seedApprovalCodeRow(t *testing.T, db *store.DB, code, docType, remark string) {
	t.Helper()
	if err := db.UpsertConfigMapping(context.Background(), store.ConfigMappingRow{
		MapKind: "approval_code", MapKey: code, MapValue: docType, Remark: remark,
	}); err != nil {
		t.Fatalf("写入 approval_code 映射失败: %v", err)
	}
}

// TestAdminApprovalDefsSyncAuthz 未授权拒绝：未登录 ⇒ 401；非系统管理员 ⇒ 403（含审计留痕）。
func TestAdminApprovalDefsSyncAuthz(t *testing.T) {
	e, db, auth, _ := newDefsSyncTestApp(t, true)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true},
		store.UserRole{OpenID: "ou_me", Role: "申请人", Department: "生产部", Active: true},
	)
	adminCookie := auth.Establish("ou_admin")
	meCookie := auth.Establish("ou_me")

	// ① 未登录（无会话 Cookie）⇒ 401（requireSession 先拦）。
	rec, _ := doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录状态码 = %d, 期望 401, body=%s", rec.Code, rec.Body.String())
	}

	// ② 已登录但非系统管理员 ⇒ 403（requireSysAdmin 拦 + deny 审计）。
	rec, _ = doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", meCookie, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非管理员状态码 = %d, 期望 403, body=%s", rec.Code, rec.Body.String())
	}

	// ③ 系统管理员 ⇒ 通过鉴权（此处清单为空，进入 400 业务校验——证明不是 403 挡的）。
	rec, env := doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", adminCookie, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("管理员空清单状态码 = %d, 期望 400, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(env.Message, "清单为空") {
		t.Fatalf("空清单错误信息应可见, got: %s", env.Message)
	}
}

// TestAdminApprovalDefsSyncPlaceholderVisible 占位符未替换 ⇒ 可见错误（400），绝不静默成功。
func TestAdminApprovalDefsSyncPlaceholderVisible(t *testing.T) {
	e, db, auth, fake := newDefsSyncTestApp(t, true)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true})
	// 模拟样例文件被绕过导入层直写进配置表（REPLACE_ME_ 占位符未替换）。
	seedApprovalCodeRow(t, db, "REPLACE_ME_approval_code_BA", "BA", "①采购报备单")
	adminCookie := auth.Establish("ou_admin")

	rec, env := doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", adminCookie, "")
	if rec.Code == http.StatusOK {
		t.Fatalf("占位符未替换不得静默成功: body=%s", rec.Body.String())
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("占位符状态码 = %d, 期望 400, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(env.Message, "占位符") {
		t.Fatalf("错误信息应指明占位符, got: %s", env.Message)
	}
	if fake.UpsertCount() != 0 {
		t.Fatalf("校验失败时不得触达飞书, upserts=%d", fake.UpsertCount())
	}
}

// TestAdminApprovalDefsSyncMissingCallbackConfig 回调 token / 域名未配置 ⇒ 可见错误（503）。
func TestAdminApprovalDefsSyncMissingCallbackConfig(t *testing.T) {
	e, db, auth, fake := newDefsSyncTestApp(t, false)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true})
	seedApprovalCodeRow(t, db, "ac-ba-001", "BA", "①采购报备单")
	adminCookie := auth.Establish("ou_admin")

	rec, env := doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", adminCookie, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("回调配置缺失状态码 = %d, 期望 503, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(env.Message, "JX_ACTION_CALLBACK_TOKEN") {
		t.Fatalf("错误信息应指明缺失的配置键, got: %s", env.Message)
	}
	if fake.UpsertCount() != 0 {
		t.Fatalf("配置缺失时不得触达飞书, upserts=%d", fake.UpsertCount())
	}
}

// TestAdminApprovalDefsSyncOK 正常装载：计数可核对、幂等、token/URL 两侧一致。
func TestAdminApprovalDefsSyncOK(t *testing.T) {
	e, db, auth, fake := newDefsSyncTestApp(t, true)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true})
	seedApprovalCodeRow(t, db, "ac-ba-001", "BA", "①采购报备单")
	seedApprovalCodeRow(t, db, "ac-pr-001", "PR", "②物资采购申请单")
	adminCookie := auth.Establish("ou_admin")

	// ① 首次装载：synced=2 / created=2。
	rec, env := doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", adminCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("首次装载状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	m := mustData(t, env)
	if m["synced"].(float64) != 2 || m["created"].(float64) != 2 || m["failed"].(float64) != 0 {
		t.Fatalf("首次装载计数不符: %v", m)
	}
	if fake.DefCount() != 2 {
		t.Fatalf("飞书侧定义数 = %d, 期望 2", fake.DefCount())
	}

	// ② 重复装载＝更新（幂等）：synced=2 / updated=2 / created=0，本地不产生第二条。
	rec, env = doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", adminCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("重复装载状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	m = mustData(t, env)
	if m["synced"].(float64) != 2 || m["updated"].(float64) != 2 || m["created"].(float64) != 0 {
		t.Fatalf("重复装载计数不符: %v", m)
	}
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_approval_def`).Scan(&n); err != nil {
		t.Fatalf("读 t_approval_def 失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("t_approval_def 行数 = %d, 期望 2（重复注册不得产生第二条）", n)
	}

	// ③ token / 回调 URL 两侧一致（本地 t_approval_def.callback_token 落同值）。
	def, err := db.GetApprovalDef(context.Background(), "ac-ba-001")
	if err != nil {
		t.Fatalf("读本地定义失败: %v", err)
	}
	if def.CallbackToken != defsSyncToken {
		t.Fatalf("callback_token = %q, 期望与 JX_ACTION_CALLBACK_TOKEN 同值", def.CallbackToken)
	}
	if def.CallbackURL != defsSyncDomain+"/approval/external/callback" {
		t.Fatalf("callback_url = %q, 期望 %s", def.CallbackURL, defsSyncDomain+"/approval/external/callback")
	}
	if def.DocType != "BA" {
		t.Fatalf("doc_type = %q, 期望 BA", def.DocType)
	}

	// ④ 快捷审批开关全链显式下发（2026-09-27 教训回归：飞书 upsert 会把未传开关重置
	// 为默认 false ⇒ 装载通道必须显式传全，否则一次装载把「同意/拒绝」两键打没）。
	extDef, err := fake.GetExternalApproval(context.Background(), "ac-ba-001")
	if err != nil {
		t.Fatalf("读飞书侧定义失败: %v", err)
	}
	if !extDef.EnableQuickOperate || !extDef.SupportPC || !extDef.SupportMobile ||
		!extDef.AllowBatchOperate || !extDef.SupportBatchRead {
		t.Fatalf("快捷审批开关未显式置 true: quick=%v pc=%v mobile=%v batch=%v batchRead=%v",
			extDef.EnableQuickOperate, extDef.SupportPC, extDef.SupportMobile,
			extDef.AllowBatchOperate, extDef.SupportBatchRead)
	}
	if extDef.EnableMarkReaded {
		t.Fatalf("enable_mark_readed 应显式为 false")
	}
	// 发起页指向回调域名根（对齐真机读回基线：http://office.hunanyichu.com:5500/）。
	if extDef.CreateLinkPC != defsSyncDomain+"/" || extDef.CreateLinkMobile != defsSyncDomain+"/" {
		t.Fatalf("create_link_pc/mobile = %q / %q, 期望 %s", extDef.CreateLinkPC, extDef.CreateLinkMobile, defsSyncDomain+"/")
	}
}
