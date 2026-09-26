package httpapi

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	jsync "github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

// Deps 路由依赖集合（由 cmd 装配）。
type Deps struct {
	Env        *config.Env
	DB         *store.DB
	Log        *slog.Logger
	Metrics    *observ.Metrics
	Health     *observ.Health
	Inbox      *inbox.Service
	Worker     *worker.Worker
	Subscriber *jsync.Subscriber
	Reconciler *jsync.Reconciler
	Perm       *permission.Loader
	Auth       *access.Authenticator
	Maps       *config.Maps
	WebUI      http.Handler
	Version    string
}

// NewRouter 装配 Echo 路由与中间件。
func NewRouter(d Deps) *echo.Echo {
	if d.Log == nil {
		d.Log = observ.NewLogger("info", nil)
	}
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = d.errorHandler

	e.Use(middleware.Recover())
	e.Use(d.traceMiddleware)
	e.Use(d.logMiddleware)
	e.Use(d.rateLimitMiddleware(300)) // 简单固定窗口限流（对应 42900）

	// ---- 内部运维端点（管理凭据域，非飞书免登）----
	internal := e.Group("", d.internalAuth)
	internal.GET("/healthz", d.handleHealthz)
	internal.GET("/readyz", d.handleReadyz)
	internal.POST("/internal/sync/subscribe", d.handleSubscribe)
	internal.POST("/internal/sync/reconcile", d.handleReconcile)
	internal.POST("/internal/events/:id/replay", d.handleReplay)
	// ★ 开发模式注入端点：仅当 DEV_MODE=true 时注册（无凭据时的端到端验证路径）。
	if d.Env != nil && d.Env.IsDev() {
		internal.POST("/internal/dev/inject-event", d.handleInjectEvent)
	}

	// ---- 免登（公开）----
	e.GET("/auth/feishu/callback", d.handleFeishuCallback)
	e.POST("/auth/logout", d.handleLogout)

	// ---- 业务接口（会话域，自动施加行·列过滤）----
	api := e.Group("/api", d.requireSession)
	api.GET("/me", d.handleMe)
	api.GET("/instances", d.handleListInstances)
	api.GET("/instances/:code", d.handleGetInstance)
	api.GET("/instances/:code/fields", d.handleInstanceFields)
	api.GET("/instances/:code/timeline", d.handleInstanceTimeline)
	api.GET("/ledger/:table", d.handleLedgerList)
	api.GET("/ledger/:table/:id", d.handleLedgerGet)
	api.PATCH("/ledger/:table/:id", d.handleLedgerPatch)
	api.GET("/audit/logs", d.handleAuditLogs)

	// ---- 系统管理（M5，★ 仅「系统管理员」；服务端二次校验，见 handlers_admin.go）----
	admin := api.Group("/admin")
	admin.GET("/permission-rules", d.handleAdminPermissionRulesGet)
	admin.PUT("/permission-rules", d.handleAdminPermissionRulesPut)
	admin.GET("/users", d.handleAdminUsersGet)
	admin.POST("/users", d.handleAdminUsersPost)
	admin.PATCH("/users/:open_id", d.handleAdminUsersPatch)

	// ---- 前端静态资源（embed 产物；缺失时 controller 降级为占位页）----
	// ★ 注意：Echo 的通配路由 "/*" 不匹配根路径 "/"，须单独注册根路由，
	//	否则访问站点根会命中 404（SPA 入口无法加载）。
	if d.WebUI != nil {
		e.GET("/", echo.WrapHandler(d.WebUI))
		e.GET("/*", echo.WrapHandler(d.WebUI))
	}
	return e
}

// ---------- 中间件 ----------

func (d Deps) traceMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		id := c.Request().Header.Get("X-Request-Id")
		if strings.TrimSpace(id) == "" {
			id = newTraceID()
		}
		c.Set(ctxKeyTraceID, id)
		c.Response().Header().Set("X-API-Version", "1")
		return next(c)
	}
}

func (d Deps) logMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		d.Log.Info("http",
			"method", c.Request().Method,
			"path", c.Request().URL.Path,
			"status", c.Response().Status,
			"duration_ms", time.Since(start).Milliseconds(),
			"trace_id", traceID(c),
		)
		return err
	}
}

// internalAuth 校验内部运维端点管理凭据（X-Internal-Token）或仅回环访问。
func (d Deps) internalAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		expect := ""
		if d.Env != nil {
			expect = strings.TrimSpace(d.Env.InternalToken)
		}
		if expect != "" {
			provided := c.Request().Header.Get("X-Internal-Token")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(expect)) == 1 {
				return next(c)
			}
			return fail(c, http.StatusUnauthorized, codeUnauthorized, "内部端点凭据无效")
		}
		// 未配置 token：仅允许回环地址访问（开发/本机运维）。
		if isLoopback(c.RealIP()) {
			return next(c)
		}
		return fail(c, http.StatusUnauthorized, codeUnauthorized, "内部端点未配置凭据且非本机访问")
	}
}

// requireSession 业务接口会话校验（未登录 → 40100）。
func (d Deps) requireSession(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ck, err := c.Cookie(access.CookieName)
		if err != nil || ck.Value == "" {
			return fail(c, http.StatusUnauthorized, codeUnauthorized, "未登录或会话失效")
		}
		sess, ok := d.Auth.ResolveSession(ck.Value)
		if !ok {
			return fail(c, http.StatusUnauthorized, codeUnauthorized, "未登录或会话失效")
		}
		c.Set(ctxKeySession, sess)
		return next(c)
	}
}

// rateLimitMiddleware 简单固定窗口限流（按 IP，1 秒窗口）。
func (d Deps) rateLimitMiddleware(perSecond int) echo.MiddlewareFunc {
	type bucket struct {
		count int
		reset time.Time
	}
	var (
		mu      sync.Mutex
		buckets = map[string]*bucket{}
	)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ip := c.RealIP()
			now := time.Now()
			mu.Lock()
			b, ok := buckets[ip]
			if !ok || now.After(b.reset) {
				b = &bucket{count: 0, reset: now.Add(time.Second)}
				buckets[ip] = b
			}
			b.count++
			exceeded := b.count > perSecond
			mu.Unlock()
			if exceeded {
				return fail(c, http.StatusTooManyRequests, codeRateLimited, "请求过于频繁")
			}
			return next(c)
		}
	}
}

// errorHandler 将错误统一包裹为 Envelope。
func (d Deps) errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	status := http.StatusInternalServerError
	code := codeInternal
	msg := "服务器内部错误"

	var he *echo.HTTPError
	if errors.As(err, &he) {
		status = he.Code
		switch he.Code {
		case http.StatusNotFound:
			code, msg = codeNotFound, "资源不存在"
		case http.StatusUnauthorized:
			code, msg = codeUnauthorized, "未登录或会话失效"
		case http.StatusForbidden:
			code, msg = codeForbidden, "无权限"
		case http.StatusTooManyRequests:
			code, msg = codeRateLimited, "请求过于频繁"
		default:
			code = he.Code * 100
			if s, ok := he.Message.(string); ok {
				msg = s
			}
		}
	}
	d.Log.Error("请求处理失败", "path", c.Request().URL.Path, "error", err.Error())
	_ = c.JSON(status, Envelope{Code: code, Data: nil, Message: msg, TraceID: traceID(c)})
}

func isLoopback(ip string) bool {
	host := strings.TrimSpace(ip)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}
