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
	fsync "github.com/chadhao/jx-procurement-platform/internal/sync"
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
	return newDefsSyncTestAppEnv(t, withCallbackConfig, nil)
}

// newDefsSyncTestAppEnv 同上，tweak 可在装配前覆写 env（N-066 用例②：单独抽空
// JX_APPROVAL_GROUP_CODE 而保留 token/域名 —— 证第五道门自身可达）。
func newDefsSyncTestAppEnv(t *testing.T, withCallbackConfig bool, tweak func(*config.Env)) (*echo.Echo, *store.DB, *access.Authenticator, *feishu.FakeExternalApprovalClient) {
	t.Helper()
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	if withCallbackConfig {
		env.CallbackDomain = defsSyncDomain
		env.ActionCallbackToken = defsSyncToken
		// ★ N-066：与 token/域名同批给全 —— 第五道门（group_code）就位后，
		//   既有「正常装载」路径须带分组 code 才能过门（本值＝测试环境实测可用值）。
		env.ApprovalGroupCode = "JXQA-GROUP-1"
	}
	if tweak != nil {
		tweak(env)
	}
	maps := &config.Maps{}

	client := &feishu.FakeClient{}
	inboxSvc := inbox.NewService(db, metrics, nil)
	ingestor := worker.NewIngestor(db, maps, nil)
	wk := worker.NewWorker(db, client, ingestor, metrics, nil)
	sub := fsync.NewSubscriber(db, client, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)
	fakeExt := feishu.NewFakeExternalApprovalClient()

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub,
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
		store.UserRole{OpenID: "ou_me", Role: "申请人", Department: "生产部", Active: true},
	)
	seedSysAdmin(t, db, "ou_admin")
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
	seedSysAdmin(t, db, "ou_admin")
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

// TestAdminApprovalDefsSyncPlaceholderMessageN065 N-065 T1①：defs/sync 占位符文案须指
// 「我方自定义的 approval_code」且**不含**旧措辞「飞书审批后台」（双断言，缺一即只证一半）。
func TestAdminApprovalDefsSyncPlaceholderMessageN065(t *testing.T) {
	e, db, auth, _ := newDefsSyncTestApp(t, true)
	seedSysAdmin(t, db, "ou_admin")
	seedApprovalCodeRow(t, db, "REPLACE_ME_approval_code_BA", "BA", "①采购报备单")
	adminCookie := auth.Establish("ou_admin")

	rec, env := doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", adminCookie, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("占位符应 400, 实为 %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(env.Message, "我方自定义的 approval_code") {
		t.Errorf("文案须指向我方自定义 code, got: %s", env.Message)
	}
	if strings.Contains(env.Message, "飞书审批后台") {
		t.Errorf("文案不得指向不存在的飞书审批后台路径（N-065 T1①）, got: %s", env.Message)
	}
}

// TestAdminApprovalDefsSyncMissingGroupCode503N066 N-066 验收②：token/域名齐备但
// JX_APPROVAL_GROUP_CODE 为空 ⇒ 第五道门 503（可预见的前置缺失当场拒装，
// 不让它去平台撞 1390001 再失败）。★ 改前无此门 ⇒ 走到装载（期望 503 实得 200/500）⇒ 红。
func TestAdminApprovalDefsSyncMissingGroupCode503N066(t *testing.T) {
	e, db, auth, fake := newDefsSyncTestAppEnv(t, true, func(env *config.Env) {
		env.ApprovalGroupCode = "" // token/域名保留 ⇒ 只抽掉分组 code
	})
	seedSysAdmin(t, db, "ou_admin")
	seedApprovalCodeRow(t, db, "jx_ba", "BA", "①采购报备单")
	adminCookie := auth.Establish("ou_admin")

	rec, env := doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", adminCookie, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("缺 group_code 应 503, 实为 %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(env.Message, "JX_APPROVAL_GROUP_CODE") ||
		!strings.Contains(env.Message, "group_code") {
		t.Errorf("503 文案须点名配置键与必填字段, got: %s", env.Message)
	}
	if fake.UpsertCount() != 0 {
		t.Errorf("门禁前置缺失不得触达飞书, upserts=%d", fake.UpsertCount())
	}
}

// TestAdminApprovalDefsSyncMissingCallbackConfig 回调 token / 域名未配置 ⇒ 可见错误（503）。
func TestAdminApprovalDefsSyncMissingCallbackConfig(t *testing.T) {
	e, db, auth, fake := newDefsSyncTestApp(t, false)
	seedSysAdmin(t, db, "ou_admin")
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
	seedSysAdmin(t, db, "ou_admin")
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
	// 发起页指向我方**按单据类型发起页**（M7/W8；历史根路径 "/" 已修正）。
	if extDef.CreateLinkPC != defsSyncDomain+"/submit/BA" || extDef.CreateLinkMobile != defsSyncDomain+"/submit/BA" {
		t.Fatalf("create_link_pc/mobile = %q / %q, 期望 %s", extDef.CreateLinkPC, extDef.CreateLinkMobile, defsSyncDomain+"/submit/BA")
	}
}

// TestAdminApprovalDefsSyncVisibleScopeDefaultN074 N-074 缺省下发面：
// 装配 DefInput 必须**显式**给 VisibleScopeJSON 可用缺省（{"viewers":[{"viewer_type":"TENANT"}]}）——
// 缺省为空 ⇒ externalApprovalBody 不下发 ⇒ 平台取默认 NONE ⇒ 定义无人可见
// （用户在飞书找不到发起入口）。★ 改前即红：fake 读回空串。
func TestAdminApprovalDefsSyncVisibleScopeDefaultN074(t *testing.T) {
	e, db, auth, fake := newDefsSyncTestApp(t, true)
	seedSysAdmin(t, db, "ou_admin")
	seedApprovalCodeRow(t, db, "ac-ba-001", "BA", "①采购报备单")
	adminCookie := auth.Establish("ou_admin")

	rec, _ := doRequest(e, http.MethodPost, "/api/admin/approval/defs/sync", adminCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("装载状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	extDef, err := fake.GetExternalApproval(context.Background(), "ac-ba-001")
	if err != nil {
		t.Fatalf("读飞书侧定义失败: %v", err)
	}
	want := `{"viewers":[{"viewer_type":"TENANT"}]}`
	if extDef.VisibleScopeJSON != want {
		t.Fatalf("下发 VisibleScopeJSON = %q, 期望缺省 %q（空 ⇒ 不下发 ⇒ 平台默认 NONE 无人可见）",
			extDef.VisibleScopeJSON, want)
	}
}

// TestAdminApprovalDefsSyncVisibleScopeOverrideN074 可配面：JX_APPROVAL_VISIBLE_SCOPE
// 覆写 ⇒ 原样透传（与 JX_APPROVAL_GROUP_CODE 同款「可配 + 缺省可用」形态），
// 不被装配处缺省覆盖。★ 实现后新增（该 Env 字段改前不存在 ⇒ 无「改前红」可得，
// 隔离性由 M2 变异展示：缺省用例红、本用例保持绿）。
func TestAdminApprovalDefsSyncVisibleScopeOverrideN074(t *testing.T) {
	e2, db2, auth2, fake2 := newDefsSyncTestAppEnv(t, true, func(env *config.Env) {
		env.ApprovalVisibleScope = `{"viewers":[{"viewer_type":"DEPARTMENT"}]}`
	})
	seedSysAdmin(t, db2, "ou_admin")
	seedApprovalCodeRow(t, db2, "ac-ba-002", "BA", "①采购报备单")
	rec2, _ := doRequest(e2, http.MethodPost, "/api/admin/approval/defs/sync", auth2.Establish("ou_admin"), "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("覆写装载状态码 = %d, body=%s", rec2.Code, rec2.Body.String())
	}
	ext2, err := fake2.GetExternalApproval(context.Background(), "ac-ba-002")
	if err != nil {
		t.Fatalf("读覆写侧定义失败: %v", err)
	}
	wantOverride := `{"viewers":[{"viewer_type":"DEPARTMENT"}]}`
	if ext2.VisibleScopeJSON != wantOverride {
		t.Errorf("覆写下发 VisibleScopeJSON = %q, 期望 %q（env 值应原样透传、不被缺省覆盖）",
			ext2.VisibleScopeJSON, wantOverride)
	}
}
