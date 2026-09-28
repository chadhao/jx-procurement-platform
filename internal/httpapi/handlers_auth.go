package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/config"
)

// ---------- 飞书免登：授权页 URL 下发 + state（防 CSRF） ----------

// oauthStateCookie 下发/回比 OAuth state 的 HttpOnly Cookie。
// ★ 官方《获取授权码》要求「务必校验 state 前后一致」以防 CSRF —— 我们把下发时的
//
//	state 存进 Cookie（短期、HttpOnly），回调时比对，不一致即拒绝。
const oauthStateCookie = "jx_oauth_state"

// oauthStateTTL state Cookie 有效期：覆盖「发起授权 → 用户在授权页停留 → 回调」窗口。
const oauthStateTTL = 10 * time.Minute

// feishuAuthorizeBaseURL 飞书授权页端点。
// ★ 官方《获取授权码》：域名是 **accounts.feishu.cn**（不是 open.feishu.cn）。
const feishuAuthorizeBaseURL = "https://accounts.feishu.cn/open-apis/authen/v1/authorize"

// newOAuthState 生成随机 state（crypto/rand 16 字节 = hex 32 字符）。
func newOAuthState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// setOAuthStateCookie 写 state Cookie（HttpOnly；生产 Secure，与 Session Cookie 同口径）。
func setOAuthStateCookie(c echo.Context, value string, env *config.Env) {
	secure := true
	if env != nil && env.IsDev() {
		secure = false // 开发模式本机 http，避免 Secure Cookie 被浏览器丢弃
	}
	c.SetCookie(&http.Cookie{
		Name:     oauthStateCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   int(oauthStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// readOAuthStateCookie 读回下发时的 state（不存在 ⇒ 空串）。
func readOAuthStateCookie(c echo.Context) string {
	ck, err := c.Cookie(oauthStateCookie)
	if err != nil {
		return ""
	}
	return ck.Value
}

// clearOAuthStateCookie 用毕即清（一次性防重放）。
func clearOAuthStateCookie(c echo.Context, env *config.Env) {
	setOAuthStateCookie(c, "", env)
}

// oauthStateMatch 常量时间比对，防时序侧信道。
func oauthStateMatch(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// handleFeishuAuthorizeURL `GET /api/auth/authorize-url`（★ 公开路由：未登录时前端
// 守卫/登录页也要能拿到，故挂 root `e`、不进 requireSession 组）。
//
// 逻辑：生成随机 state（crypto/rand ≥16 字节，存 HttpOnly Cookie 供回调比对）
// → 按官方《获取授权码》拼装授权页 URL → 返回 {"authorize_url": "..."}。
// 未配置凭据 / 回调地址 ⇒ **可见报错**，不静默编造。
//
// 前置条件（开放平台侧，见 docs/05-API.md §1.1）：重定向 URL 白名单须含本回调地址、
// redirect_uri 不得含 `#`、所需 scope 已开通（未开通的 scope 用户侧报 20027）。
func (d Deps) handleFeishuAuthorizeURL(c echo.Context) error {
	if d.Env == nil {
		return fail(c, http.StatusInternalServerError, codeInternal, "服务配置缺失（Env 为空）")
	}
	clientID := strings.TrimSpace(d.Env.AppID)
	if clientID == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"未配置 JX_APP_ID，无法发起飞书免登")
	}
	redirectURI := strings.TrimSpace(d.Env.FeishuRedirectURI())
	if redirectURI == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"未配置免登回调地址（JX_OAUTH_REDIRECT_URI 或 JX_CALLBACK_DOMAIN），无法发起飞书免登")
	}
	// 官方提示：redirect_uri 含 # 时 fragment 会被拼到回调末尾（SPA 取不到 code）。
	if strings.Contains(redirectURI, "#") {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"免登回调地址不得包含 #（官方：fragment 会被拼到回调末尾），请改用不含 # 的地址")
	}

	state, err := newOAuthState()
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, "生成 state 失败: "+err.Error())
	}
	setOAuthStateCookie(c, state, d.Env)

	// 官方参数：client_id（必）、response_type=code（必）、redirect_uri（必，URL 编码）、
	// state（否但官方要求前后一致校验，故必带）。scope 不拼（未开通的权限会报 20027）。
	u, err := url.Parse(feishuAuthorizeBaseURL)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, "授权页 URL 组装失败")
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	u.RawQuery = q.Encode()

	return ok(c, map[string]any{"authorize_url": u.String()})
}
