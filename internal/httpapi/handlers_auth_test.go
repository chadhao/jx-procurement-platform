package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	jsync "github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

// fakeOAuth 免登换身份的假实现（access.Authenticator 依赖 OAuthExchange 接口）。
type fakeOAuth struct {
	ident feishu.FeishuIdentity
	err   error
}

func (f fakeOAuth) ExchangeCode(_ context.Context, _ string) (feishu.FeishuIdentity, error) {
	return f.ident, f.err
}

// newAuthTestApp 装配免登链路专用最小路由（env / oauth 由用例定制）。
func newAuthTestApp(t *testing.T, env *config.Env, oauth feishu.OAuthExchange) (*echo.Echo, *access.Authenticator, *store.DB) {
	t.Helper()
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	maps := &config.Maps{}

	client := &feishu.FakeClient{}
	inboxSvc := inbox.NewService(db, metrics, nil)
	ingestor := worker.NewIngestor(db, maps, nil)
	wk := worker.NewWorker(db, client, ingestor, metrics, nil)
	sub := jsync.NewSubscriber(db, client, maps, metrics, nil)
	rec := jsync.NewReconciler(db, client, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, oauth, env.DevMode, nil)

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub, Reconciler: rec,
		Perm: perm, Auth: auth, Maps: maps, WebUI: nil, Version: "test",
	})
	return e, auth, db
}

// cookieValue 从响应中取指定名称的 Cookie 值。
func cookieValue(t *testing.T, rec interface{ Result() *http.Response }, name string) string {
	t.Helper()
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

const testRedirectURI = "https://jx.example.com/auth/feishu/callback"

// TestAuthorizeURLContent 授权 URL 必含官方授权页端点与全部官方参数（★ accounts.feishu.cn）。
func TestAuthorizeURLContent(t *testing.T) {
	e, _, _ := newAuthTestApp(t, &config.Env{
		AppID:            "cli_a5d611352af9d00b",
		OAuthRedirectURI: testRedirectURI,
	}, nil)

	rec, env := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("authorize-url: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	rawURL, _ := data["authorize_url"].(string)
	if rawURL == "" {
		t.Fatal("authorize_url 为空")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("authorize_url 不可解析: %v", err)
	}
	// ★ 官方《获取授权码》：域名 accounts.feishu.cn（非 open.feishu.cn）。
	if u.Scheme != "https" || u.Host != "accounts.feishu.cn" || u.Path != "/open-apis/authen/v1/authorize" {
		t.Errorf("授权页端点错误: %s", rawURL)
	}
	q := u.Query()
	if q.Get("client_id") != "cli_a5d611352af9d00b" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, 期望 code", q.Get("response_type"))
	}
	// redirect_uri 必须 URL 编码且与配置一致。
	if q.Get("redirect_uri") != testRedirectURI {
		t.Errorf("redirect_uri = %q, 期望 %q（URL 解码后）", q.Get("redirect_uri"), testRedirectURI)
	}
	if !strings.Contains(rawURL, "redirect_uri=https%3A%2F%2Fjx.example.com%2Fauth%2Ffeishu%2Fcallback") {
		t.Errorf("redirect_uri 未按 URL 编码: %s", rawURL)
	}
	if len(q.Get("state")) < 32 { // 16 字节 hex = 32 字符
		t.Errorf("state 过短: %q", q.Get("state"))
	}
	// state 须同时写入 HttpOnly Cookie 供回调比对。
	if st := cookieValue(t, rec, oauthStateCookie); st != q.Get("state") {
		t.Errorf("cookie state = %q, 与 URL state %q 不一致", st, q.Get("state"))
	}
}

// TestAuthorizeURLFallbackToCallbackDomain 未配 JX_OAUTH_REDIRECT_URI ⇒ 缺省
// JX_CALLBACK_DOMAIN + /auth/feishu/callback。
func TestAuthorizeURLFallbackToCallbackDomain(t *testing.T) {
	e, _, _ := newAuthTestApp(t, &config.Env{
		AppID:          "cli_x",
		CallbackDomain: "https://jx.example.com/",
	}, nil)

	_, env := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
	data := mustData(t, env)
	rawURL, _ := data["authorize_url"].(string)
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("authorize_url 不可解析: %v", err)
	}
	if got := u.Query().Get("redirect_uri"); got != testRedirectURI {
		t.Errorf("redirect_uri = %q, 期望缺省拼接 %q", got, testRedirectURI)
	}
}

// TestAuthorizeURLMissingConfig 未配置凭据/回调地址 ⇒ 可见 400 报错（不静默编造）。
func TestAuthorizeURLMissingConfig(t *testing.T) {
	e, _, _ := newAuthTestApp(t, &config.Env{}, nil)

	rec, env := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Errorf("未配置时应 400/40000, 实际 http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	if !strings.Contains(env.Message, "JX_APP_ID") && !strings.Contains(env.Message, "回调地址") {
		t.Errorf("错误信息应可见指出缺什么: %s", env.Message)
	}
}

// TestAuthorizeURLRejectsFragmentCallback redirect_uri 含 # ⇒ 可见拒绝（官方：fragment
// 会被拼到回调末尾，SPA 取不到 code）。
func TestAuthorizeURLRejectsFragmentCallback(t *testing.T) {
	e, _, _ := newAuthTestApp(t, &config.Env{
		AppID:            "cli_x",
		OAuthRedirectURI: "https://jx.example.com/#/auth/callback",
	}, nil)

	rec, env := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("含 # 的回调地址应 400, 实际 %d", rec.Code)
	}
	if !strings.Contains(env.Message, "#") {
		t.Errorf("错误信息应说明 # 问题: %s", env.Message)
	}
}

// TestCallbackStateMismatchRejected state 与下发 Cookie 不一致 / 缺 Cookie ⇒ 拒绝（防 CSRF）。
func TestCallbackStateMismatchRejected(t *testing.T) {
	e, _, _ := newAuthTestApp(t, &config.Env{
		DevMode:          true,
		AppID:            "cli_x",
		OAuthRedirectURI: testRedirectURI,
	}, fakeOAuth{ident: feishu.FeishuIdentity{OpenID: "ou_x"}})

	// 先拿到合法的 state Cookie。
	rec, _ := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
	validState := cookieValue(t, rec, oauthStateCookie)
	if validState == "" {
		t.Fatal("authorize-url 未下发 state Cookie")
	}

	// ① state 与 Cookie 不一致 → 400。
	req := httptest.NewRequest(http.MethodGet, "/auth/feishu/callback?code=c1&state=tampered", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: validState})
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("state 不一致应 400, 实际 %d", rec2.Code)
	}

	// ② 无 state Cookie（未走 authorize-url）→ 400。
	rec3 := httptest.NewRecorder()
	e.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/auth/feishu/callback?code=c1&state="+validState, nil))
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("缺 state Cookie 应 400, 实际 %d", rec3.Code)
	}
}

// TestCallbackStateMatchEstablishesSession state 匹配 ⇒ 放行：302 → / 并建立会话。
func TestCallbackStateMatchEstablishesSession(t *testing.T) {
	e, auth, db := newAuthTestApp(t, &config.Env{
		AppID:            "cli_x",
		OAuthRedirectURI: testRedirectURI,
	}, fakeOAuth{ident: feishu.FeishuIdentity{OpenID: "ou_x"}})
	if err := db.UpsertUserRole(context.Background(),
		store.UserRole{OpenID: "ou_x", Role: "申请人", Active: true}); err != nil {
		t.Fatalf("写入用户角色失败: %v", err)
	}

	rec, _ := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
	state := cookieValue(t, rec, oauthStateCookie)

	req := httptest.NewRequest(http.MethodGet, "/auth/feishu/callback?code=real-code&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: state})
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusFound {
		t.Fatalf("state 匹配应 302, 实际 %d body=%s", rec2.Code, rec2.Body.String())
	}
	if loc := rec2.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, 期望 /", loc)
	}
	// 会话 Cookie 已建立，且 state Cookie 已被清除（一次性防重放）。
	if sess := cookieValue(t, rec2, access.CookieName); sess == "" {
		t.Error("成功回调未建立会话 Cookie")
	}
	if got := cookieValue(t, rec2, oauthStateCookie); got != "" {
		t.Errorf("state Cookie 应已清除, 实际 %q", got)
	}
	_ = auth
}

// TestCallbackAccessDeniedFriendly 用户在授权页拒绝（官方 error=access_denied）⇒
// 友好 302 到 /login?error=denied，绝不 500。
func TestCallbackAccessDeniedFriendly(t *testing.T) {
	e, _, _ := newAuthTestApp(t, &config.Env{AppID: "cli_x"}, nil)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/auth/feishu/callback?error=access_denied&state=abc", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("access_denied 应 302 友好处理, 实际 %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?error=denied" {
		t.Errorf("Location = %q, 期望 /login?error=denied", loc)
	}
}

// TestCallbackDevOpenIDDirectRegression 回归：DEV_MODE 的 ?open_id= 直连路径不被 state
// 新校验破坏（现有测试与开发入口依赖）。
func TestCallbackDevOpenIDDirectRegression(t *testing.T) {
	e, _, db := newAuthTestApp(t, &config.Env{DevMode: true}, nil) // oauth=nil：开发模式无需凭据
	if err := db.UpsertUserRole(context.Background(),
		store.UserRole{OpenID: "ou_dev_001", Role: "申请人", Active: true}); err != nil {
		t.Fatalf("写入用户角色失败: %v", err)
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/auth/feishu/callback?state=devlogi&open_id=ou_dev_001", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("DEV 直连应 302, 实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if cookieValue(t, rec, access.CookieName) == "" {
		t.Error("DEV 直连未建立会话 Cookie")
	}
}
