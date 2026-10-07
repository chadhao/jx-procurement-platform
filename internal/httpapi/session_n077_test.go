package httpapi

// session_n077_test.go —— N-077 判据③⑤ 的 HTTP 侧：
// ③ 滑动续期与 cookie **同批**（续期请求的 Set-Cookie Max-Age 同步延长；节流窗内不重发）；
// ⑤ 安全属性不变（HttpOnly / SameSite=Lax / 生产 Secure）＋ 登出后旧 cookie 立即失效。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/labstack/echo/v4"
)

// n076SessionID 从 cookie 值取会话 id（值 = id + "." + HMAC）。
func n077SessionID(t *testing.T, cookie string) string {
	t.Helper()
	i := strings.IndexByte(cookie, '.')
	if i <= 0 {
		t.Fatalf("cookie 值形态异常: %q", cookie)
	}
	return cookie[:i]
}

// n077Backdate 把 t_session 行的续期基准回拨（模拟「距上次续期 ≥ 5 分钟」——
// 默认节流窗内不会续期，必须回拨才能触发判据③ 的续期路径）。
func n077Backdate(t *testing.T, db *store.DB, id string, updatedAgo, expiresIn time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	_, err := db.ExecContext(context.Background(),
		`UPDATE t_session SET updated_at = ?, expires_at = ? WHERE id = ?`,
		now.Add(-updatedAgo).Format(time.RFC3339),
		now.Add(expiresIn).Format(time.RFC3339),
		id,
	)
	if err != nil {
		t.Fatalf("回拨会话行失败: %v", err)
	}
}

// TestSessionRenewalCookieBatchN077 判据③：续期发生 ⇒ 同一响应 Set-Cookie
// Max-Age 按 SessionTTL 延长（测试 Env 直构 SessionTTL=0 ⇒ 缺省 12h=43200）；
// 紧接着的第二次请求（节流窗内）⇒ **不**重发 Set-Cookie。
func TestSessionRenewalCookieBatchN077(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_me", Role: "申请人", Active: true})
	tok := auth.Establish("ou_me")
	n077Backdate(t, db, n077SessionID(t, tok), 6*time.Hour, 6*time.Hour)

	// ① 续期请求：Set-Cookie Max-Age=43200（12h 缺省）＋ HttpOnly ＋ SameSite=Lax。
	rec, env := doRequest(e, http.MethodGet, "/api/me", tok, "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("续期请求: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	sc := rec.Header().Values("Set-Cookie")
	var renewed string
	for _, v := range sc {
		if strings.HasPrefix(v, access.CookieName+"=") {
			renewed = v
		}
	}
	if renewed == "" {
		t.Fatalf("续期响应缺 Set-Cookie（服务端续期与 cookie 未同批 —— 判据③）: %v", sc)
	}
	if !strings.Contains(renewed, "Max-Age=43200") {
		t.Errorf("续期 Set-Cookie 应带 Max-Age=43200（SessionTTL 缺省 12h），实为: %s", renewed)
	}
	if !strings.Contains(renewed, "HttpOnly") || !strings.Contains(renewed, "SameSite=Lax") {
		t.Errorf("续期 Set-Cookie 安全属性缺失（HttpOnly/Lax 必须保持）: %s", renewed)
	}

	// ② 节流窗内第二次请求：不重发 Set-Cookie（不每请求一写、不每请求一发）。
	rec2, env2 := doRequest(e, http.MethodGet, "/api/me", tok, "")
	if rec2.Code != http.StatusOK || env2.Code != codeOK {
		t.Fatalf("节流窗内请求: http=%d code=%d", rec2.Code, env2.Code)
	}
	for _, v := range rec2.Header().Values("Set-Cookie") {
		if strings.HasPrefix(v, access.CookieName+"=") {
			t.Errorf("节流窗内不应重发会话 cookie: %s", v)
		}
	}
}

// TestSessionLogoutInvalidatesN077 判据⑤：登出 ⇒ cookie 清除（Max-Age=0）且
// **旧 cookie 立即失效**（Destroy 落库，见 access#TestSessionDestroyPersistentN077）。
func TestSessionLogoutInvalidatesN077(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_me", Role: "申请人", Active: true})
	tok := auth.Establish("ou_me")

	// 前置：有效。
	rec1, env1 := doRequest(e, http.MethodGet, "/api/me", tok, "")
	if rec1.Code != http.StatusOK || env1.Code != codeOK {
		t.Fatalf("前置 /api/me: http=%d", rec1.Code)
	}

	// 登出。
	recL, envL := doRequest(e, http.MethodPost, "/auth/logout", tok, "")
	if recL.Code != http.StatusOK || envL.Code != codeOK {
		t.Fatalf("登出: http=%d code=%d body=%s", recL.Code, envL.Code, recL.Body.String())
	}
	cleared := false
	for _, v := range recL.Header().Values("Set-Cookie") {
		if strings.HasPrefix(v, access.CookieName+"=") && (strings.Contains(v, "Max-Age=0") || strings.Contains(v, "Max-Age=-1")) {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("登出响应应清 cookie（Max-Age=0/-1），实为: %v", recL.Header().Values("Set-Cookie"))
	}

	// 旧 cookie 立即失效。
	rec2, _ := doRequest(e, http.MethodGet, "/api/me", tok, "")
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("登出后旧 cookie: http=%d, 期望 401（body=%s）", rec2.Code, rec2.Body.String())
	}
}

// TestSessionCookieSecurityAttrsN077 判据⑤（属性面）：HttpOnly / SameSite=Lax /
// 生产 Secure=true / 开发 Secure=false —— setSessionCookie 语义保持不变。
func TestSessionCookieSecurityAttrsN077(t *testing.T) {
	read := func(env *config.Env) *http.Cookie {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		setSessionCookie(c, "v-test", env, 60)
		cks := rec.Result().Cookies()
		if len(cks) != 1 {
			t.Fatalf("cookie 数 = %d", len(cks))
		}
		return cks[0]
	}
	prod := read(&config.Env{DevMode: false})
	if !prod.HttpOnly || prod.SameSite != http.SameSiteLaxMode || !prod.Secure {
		t.Errorf("生产属性应为 HttpOnly+Lax+Secure, 实为 HttpOnly=%v SameSite=%v Secure=%v",
			prod.HttpOnly, prod.SameSite, prod.Secure)
	}
	dev := read(&config.Env{DevMode: true})
	if dev.Secure {
		t.Error("开发模式 Secure 应为 false（本机 http，避免 cookie 被浏览器丢弃）")
	}
	if !dev.HttpOnly || dev.SameSite != http.SameSiteLaxMode {
		t.Error("开发模式 HttpOnly/Lax 不得放松")
	}
}

// TestSessionTTLSecondsHelperN077 判据④（cookie 半边）：sessionTTLSeconds ——
// env 配置生效；零值/nil ⇒ 缺省 12h（43200）。
func TestSessionTTLSecondsHelperN077(t *testing.T) {
	if got := sessionTTLSeconds(&config.Env{SessionTTL: 2 * time.Hour}); got != 7200 {
		t.Errorf("SessionTTL=2h ⇒ %d 秒, 期望 7200", got)
	}
	if got := sessionTTLSeconds(&config.Env{}); got != 43200 {
		t.Errorf("零值 Env ⇒ %d 秒, 期望缺省 43200（12h）", got)
	}
	if got := sessionTTLSeconds(nil); got != 43200 {
		t.Errorf("nil Env ⇒ %d 秒, 期望缺省 43200", got)
	}
}
