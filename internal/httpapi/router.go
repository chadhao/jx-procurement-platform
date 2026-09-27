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
	"github.com/chadhao/jx-procurement-platform/internal/approval"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/objectstore"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
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
	// Flow 审批领域服务（架构转向 ③ 上电，T03b/T04b）：页面两键 / 四操作 / 待办 / 入站回调。
	// ★ 回调与页面两键**走同一状态机出口**（docs/11 R11），故共用此一个服务实例。
	Flow *flow.Service
	// ApprovalDefs 三方审批定义注册表（approval.Registry + feishu.ExternalApprovalClient）。
	// 装配即用：定义注册/更新经此实例；对应的管理端点另行排期（本批不新增路由，避免与 docs 漂移）。
	ApprovalDefs *approval.Registry
	// ApprovalReconciler 审批对账器（T03）：`POST /internal/approval/check` 唯一入口（R24）。
	ApprovalReconciler *jsync.ApprovalReconciler
	Perm               *permission.Loader
	Auth               *access.Authenticator
	Maps               *config.Maps
	WebUI              http.Handler
	Version            string
	// Feishu 飞书客户端：附件**按需拉取**需要（B39）。为 nil 时下载端点返回 502。
	Feishu feishu.Client
	// Objects 附件对象存储（主存）。为 nil 时**不缓存、直接转发**（降级，不是静默丢功能）。
	Objects objectstore.Store
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
	// ★ 审批对账唯一入口（R24）：对 external_instances/check 的 diff 判方向后重推。
	//   与旧 `/internal/sync/reconcile`（410 Gone）是**两个不同职能**，不重复。
	internal.POST("/internal/approval/check", d.handleApprovalCheck)
	internal.POST("/internal/events/:id/replay", d.handleReplay)
	// ★ 开发模式注入端点：仅当 DEV_MODE=true 时注册（无凭据时的端到端验证路径）。
	if d.Env != nil && d.Env.IsDev() {
		internal.POST("/internal/dev/inject-event", d.handleInjectEvent)
	}

	// ---- 免登（公开）----
	e.GET("/auth/feishu/callback", d.handleFeishuCallback)
	e.POST("/auth/logout", d.handleLogout)

	// ---- ★ 入站回调（飞书 → 我方；独立入站面）----
	// ★★ 必须挂 **root `e`**、与 `/auth/*` 同组，**绝不进 `api` 组** —— 飞书回调请求
	//    **无会话 cookie**，若挂 `requireSession` 会 401 全失败且静默（docs/11 R14 / docs/05 §3.14）。
	//    该路径**不走** api 组、不施会话中间件；业务层凭 action_callback_token 校验（flow.HandleCallback）。
	e.POST("/approval/external/callback", d.handleExternalApprovalCallback)

	// ---- 业务接口（会话域，自动施加行·列过滤）----
	api := e.Group("/api", d.requireSession)
	api.GET("/me", d.handleMe)
	api.GET("/instances", d.handleListInstances)
	api.GET("/instances/:code", d.handleGetInstance)
	api.GET("/instances/:code/fields", d.handleInstanceFields)
	api.GET("/instances/:code/timeline", d.handleInstanceTimeline)
	// 附件（B39）：元数据列表 + 按需下载（行级以所属实例为准）。
	api.GET("/instances/:code/attachments", d.handleInstanceAttachments)
	api.GET("/attachment/:file_id", d.handleAttachmentDownload)
	api.GET("/ledger/:table", d.handleLedgerList)
	api.GET("/ledger/:table/:id", d.handleLedgerGet)
	api.PATCH("/ledger/:table/:id", d.handleLedgerPatch)
	api.GET("/audit/logs", d.handleAuditLogs)

	// ---- 审批流转（架构转向 ③；T04b；docs/05-API §3.13）----
	//	★ 页面两键与入站回调**走同一状态机出口**（flow），不得两套语义（docs/11 R11）。
	//	★ 路径参数 `:biz_no` ＝**业务单号**（非 instance_id）。
	api.POST("/approval/:biz_no/approve", d.handleApprovalApprove)
	api.POST("/approval/:biz_no/reject", d.handleApprovalReject)
	// 四操作（转交 / 加签 / 回退 / 撤回）；★ 加签另带 timing ∈ {AFTER, BEFORE}（缺省 AFTER）。
	api.POST("/approval/:biz_no/transfer", d.handleApprovalTransfer)
	api.POST("/approval/:biz_no/addsign", d.handleApprovalAddSign)
	api.POST("/approval/:biz_no/rollback", d.handleApprovalRollback)
	api.POST("/approval/:biz_no/cancel", d.handleApprovalCancel)
	// 我的待办（★ 命名已定 ＝ `/tasks`，非 `/todo`）。
	api.GET("/approval/tasks", d.handleApprovalTasks)

	// ---- 看板（M5，只读；行级过滤在 SQL 层、列级投影在序列化层）----
	//	★ dashboard:{1..4} 权限资源按看板 id 分派，见 handlers_dashboard.go / docs/05-API.md §3.3。
	api.GET("/dashboard/:id", d.handleDashboard)
	api.GET("/dashboard/:id/export", d.handleDashboardExport)

	// ---- 备付金（M1）/ 报送（M6）----
	//	★ 两资源暂以角色口径显式鉴权（authorizeRole），不进入行·列权限矩阵，
	//	  避免出现「矩阵可配但处理器更严」的假配置；待资源枚举入库后平滑迁移。
	api.GET("/petty-cash/balance", d.handlePettyCashBalance)
	api.POST("/petty-cash/receipt", d.handlePettyCashReceipt)
	api.POST("/petty-cash/monthly-close", d.handlePettyCashMonthlyClose)
	api.POST("/submission", d.handleCreateSubmission)
	api.GET("/submission", d.handleListSubmissions)
	api.GET("/submission/:id/package", d.handleSubmissionPackage)
	api.POST("/submission/:id/receipt", d.handleRegisterSubmissionReceipt)
	api.POST("/submission/:id/group", d.handleRegisterSubmissionGroup)
	api.POST("/submission/:id/reject", d.handleRejectSubmission)

	// ---- 集团报销跟踪表（M1，FR-M1-02；同上：角色口径显式鉴权，不进权限矩阵）----
	api.POST("/reimbursement", d.handleCreateReimbursement)
	api.GET("/reimbursement", d.handleListReimbursements)
	api.PATCH("/reimbursement/:id", d.handlePatchReimbursement)

	// ---- 变更链回溯（M4，FR-M4-07；按合同号查历次变更，行级过滤在 SQL 层）----
	api.GET("/contract/:biz_no/changes", d.handleContractChanges)

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
