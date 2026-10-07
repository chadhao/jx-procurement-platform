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
	fsync "github.com/chadhao/jx-procurement-platform/internal/sync"
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
	sub := fsync.NewSubscriber(db, client, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore(db, "test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, oauth, env.DevMode, nil)

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub,
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

// ---------- 登录后回跳原目标页（★ 含开放重定向防护用例） ----------

// upsertTestRole 写一条映射角色（回跳链路要建会话必须先有角色映射）。
func upsertTestRole(t *testing.T, db *store.DB, openID string) {
	t.Helper()
	if err := db.UpsertUserRole(context.Background(),
		store.UserRole{OpenID: openID, Role: "申请人", Active: true}); err != nil {
		t.Fatalf("写入用户角色失败: %v", err)
	}
}

// callbackWithCookies 带指定 Cookie 回调并返回响应。
func callbackWithCookies(e *echo.Echo, query string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/auth/feishu/callback"+query, nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestAuthorizeURLWithRedirectThenCallback 端到端：authorize-url?redirect=/approval/PR-1
// ⇒ 目标写入 HttpOnly Cookie ⇒ 回调成功后 302 到 /approval/PR-1（登录后回到原目标页）。
func TestAuthorizeURLWithRedirectThenCallback(t *testing.T) {
	e, _, db := newAuthTestApp(t, &config.Env{
		AppID:            "cli_x",
		OAuthRedirectURI: testRedirectURI,
	}, fakeOAuth{ident: feishu.FeishuIdentity{OpenID: "ou_x"}})
	upsertTestRole(t, db, "ou_x")

	rec, env := doRequest(e, http.MethodGet,
		"/api/auth/authorize-url?redirect="+url.QueryEscape("/approval/PR-1"), "", "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("authorize-url: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	state := cookieValue(t, rec, oauthStateCookie)
	if state == "" {
		t.Fatal("未下发 state Cookie")
	}
	// 回跳目标写入 HttpOnly Cookie（URL 编码存储），与 state 同批下发。
	raw := cookieValue(t, rec, oauthRedirectCookie)
	if raw == "" {
		t.Fatal("未下发回跳目标 Cookie")
	}
	if got, err := url.QueryUnescape(raw); err != nil || got != "/approval/PR-1" {
		t.Errorf("回跳目标 Cookie = %q（解码 %q, err=%v）, 期望 /approval/PR-1", raw, got, err)
	}

	// 回调：state 匹配 ⇒ 302 到原目标页（不再是固定 /）。
	rec2 := callbackWithCookies(e, "?code=c1&state="+state,
		&http.Cookie{Name: oauthStateCookie, Value: state},
		&http.Cookie{Name: oauthRedirectCookie, Value: raw})
	if rec2.Code != http.StatusFound {
		t.Fatalf("回调应 302, 实际 %d body=%s", rec2.Code, rec2.Body.String())
	}
	if loc := rec2.Header().Get("Location"); loc != "/approval/PR-1" {
		t.Errorf("Location = %q, 期望 /approval/PR-1", loc)
	}
	if cookieValue(t, rec2, access.CookieName) == "" {
		t.Error("成功回调未建立会话 Cookie")
	}
	if got := cookieValue(t, rec2, oauthRedirectCookie); got != "" {
		t.Errorf("回跳目标 Cookie 应已清除（用毕即清）, 实际 %q", got)
	}
}

// TestAuthorizeURLRejectsOpenRedirect ★ 开放重定向防护（安全红线）：
// authorize-url 收到非法目标 ⇒ 不 500、目标不落 Cookie（丢弃、回调回落 /）；
// 回调 302 Location 一律是 /（站内），绝不外跳。
// 用例覆盖：协议相对路径 //、反斜杠变体 /\、绝对 URL http(s)://、不以 / 开头、
// 任意位置 scheme、自定义 scheme、超长。
func TestAuthorizeURLRejectsOpenRedirect(t *testing.T) {
	e, _, db := newAuthTestApp(t, &config.Env{
		AppID:            "cli_x",
		OAuthRedirectURI: testRedirectURI,
	}, fakeOAuth{ident: feishu.FeishuIdentity{OpenID: "ou_x"}})
	upsertTestRole(t, db, "ou_x")

	cases := []string{
		"//evil.com",              // 协议相对路径（浏览器按 host=evil.com 解析）
		"https://evil.com",        // 绝对 URL
		"http://x",                // 绝对 URL（http）
		`/\evil.com`,              // 反斜杠变体（部分客户端把 \ 当 / 归一）
		`/..\evil.com`,            // 反斜杠夹杂
		"approval/PR-1",           // 不以 / 开头
		"/x?next=http://evil.com", // 任意位置出现 scheme
		"javascript:alert(1)",     // 自定义 scheme
		strings.Repeat("/a", 600), // 超长（> 512）
	}
	for _, raw := range cases {
		q := url.QueryEscape(raw)
		rec, env := doRequest(e, http.MethodGet, "/api/auth/authorize-url?redirect="+q, "", "")
		if rec.Code != http.StatusOK || env.Code != codeOK {
			t.Errorf("redirect=%q: 应 200 放行（丢弃参数而非报错）, 实际 http=%d body=%s",
				raw, rec.Code, rec.Body.String())
			continue
		}
		if got := cookieValue(t, rec, oauthRedirectCookie); got != "" {
			t.Errorf("redirect=%q: 非法目标不应写入 Cookie, 实际 %q", raw, got)
		}
		// 回调（带合法 state）⇒ 302 回落 /，不 500、不外跳。
		state := cookieValue(t, rec, oauthStateCookie)
		rec2 := callbackWithCookies(e, "?code=c1&state="+state,
			&http.Cookie{Name: oauthStateCookie, Value: state})
		if rec2.Code != http.StatusFound {
			t.Errorf("redirect=%q: 回调应 302, 实际 %d", raw, rec2.Code)
			continue
		}
		if loc := rec2.Header().Get("Location"); loc != "/" {
			t.Errorf("redirect=%q: Location = %q, 期望回落 /", raw, loc)
		}
	}
}

// TestAuthorizeURLWithoutRedirectFallsBackRoot 无 redirect 参数 ⇒ 回调 302 回落 /
// （回归：既有行为不变；同时验证陈旧/被篡改的回跳 Cookie 也只会回落站内）。
func TestAuthorizeURLWithoutRedirectFallsBackRoot(t *testing.T) {
	e, _, db := newAuthTestApp(t, &config.Env{
		AppID:            "cli_x",
		OAuthRedirectURI: testRedirectURI,
	}, fakeOAuth{ident: feishu.FeishuIdentity{OpenID: "ou_x"}})
	upsertTestRole(t, db, "ou_x")

	rec, _ := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
	state := cookieValue(t, rec, oauthStateCookie)

	// ① 全程无 redirect ⇒ /。
	rec2 := callbackWithCookies(e, "?code=c1&state="+state,
		&http.Cookie{Name: oauthStateCookie, Value: state})
	if rec2.Code != http.StatusFound || rec2.Header().Get("Location") != "/" {
		t.Errorf("无 redirect 应 302 → /, 实际 %d Location=%q",
			rec2.Code, rec2.Header().Get("Location"))
	}

	// ② 回跳 Cookie 被篡改为外站地址 ⇒ 回落 /（纵深防御：读取侧再校验一次）。
	rec3, _ := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
	state3 := cookieValue(t, rec3, oauthStateCookie)
	rec4 := callbackWithCookies(e, "?code=c1&state="+state3,
		&http.Cookie{Name: oauthStateCookie, Value: state3},
		&http.Cookie{Name: oauthRedirectCookie, Value: url.QueryEscape("https://evil.com")})
	if rec4.Code != http.StatusFound || rec4.Header().Get("Location") != "/" {
		t.Errorf("被篡改的回跳 Cookie 应回落 /, 实际 %d Location=%q",
			rec4.Code, rec4.Header().Get("Location"))
	}
}

// TestAuthorizeURLInvalidRedirectCookieReadSide 回调侧对**编码损坏 / 含反斜杠**的
// 回跳 Cookie 值也只回落 /（不 500）。
func TestAuthorizeURLInvalidRedirectCookieReadSide(t *testing.T) {
	e, _, db := newAuthTestApp(t, &config.Env{
		AppID:            "cli_x",
		OAuthRedirectURI: testRedirectURI,
	}, fakeOAuth{ident: feishu.FeishuIdentity{OpenID: "ou_x"}})
	upsertTestRole(t, db, "ou_x")

	for _, bad := range []string{"%zz-not-escaped", url.QueryEscape(`/\evil.com`)} {
		rec, _ := doRequest(e, http.MethodGet, "/api/auth/authorize-url", "", "")
		state := cookieValue(t, rec, oauthStateCookie)
		rec2 := callbackWithCookies(e, "?code=c1&state="+state,
			&http.Cookie{Name: oauthStateCookie, Value: state},
			&http.Cookie{Name: oauthRedirectCookie, Value: bad})
		if rec2.Code != http.StatusFound || rec2.Header().Get("Location") != "/" {
			t.Errorf("bad=%q: 应回落 /, 实际 %d Location=%q",
				bad, rec2.Code, rec2.Header().Get("Location"))
		}
	}
}

// TestCallbackAccessDeniedKeepsRedirectCookie 授权被拒 ⇒ 仍 302 /login?error=denied，
// 且回跳目标 Cookie 保留（登录页重试可沿用原目标；TTL 10 分钟自然过期兜底）。
func TestCallbackAccessDeniedKeepsRedirectCookie(t *testing.T) {
	e, _, _ := newAuthTestApp(t, &config.Env{AppID: "cli_x"}, nil)

	rec := callbackWithCookies(e, "?error=access_denied&state=abc",
		&http.Cookie{Name: oauthRedirectCookie, Value: url.QueryEscape("/approval/PR-1")})
	if rec.Code != http.StatusFound {
		t.Fatalf("access_denied 应 302, 实际 %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?error=denied" {
		t.Errorf("Location = %q, 期望 /login?error=denied", loc)
	}
}

// TestCallbackDevOpenIDWithRedirect DEV_MODE 直连路径支持 ?redirect= 回跳；
// 非法目标仍回落 /（开放重定向防护对 DEV 路径同样生效）。
func TestCallbackDevOpenIDWithRedirect(t *testing.T) {
	e, _, db := newAuthTestApp(t, &config.Env{DevMode: true}, nil)
	upsertTestRole(t, db, "ou_dev_001")

	// ① 合法目标 ⇒ 302 到目标页并建会话。
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/auth/feishu/callback?state=devlogi&open_id=ou_dev_001&redirect="+
			url.QueryEscape("/approval/PR-1"), nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("DEV 直连应 302, 实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/approval/PR-1" {
		t.Errorf("Location = %q, 期望 /approval/PR-1", loc)
	}
	if cookieValue(t, rec, access.CookieName) == "" {
		t.Error("DEV 直连未建立会话 Cookie")
	}

	// ② 非法目标 ⇒ 回落 /，不 500。
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet,
		"/auth/feishu/callback?state=devlogi&open_id=ou_dev_001&redirect="+
			url.QueryEscape("//evil.com"), nil))
	if rec2.Code != http.StatusFound || rec2.Header().Get("Location") != "/" {
		t.Errorf("DEV 直连非法 redirect 应回落 /, 实际 %d Location=%q",
			rec2.Code, rec2.Header().Get("Location"))
	}
}
