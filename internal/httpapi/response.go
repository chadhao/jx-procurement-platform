// Package httpapi 实现 Echo HTTP 路由与处理器（接入层）。
// 契约见 docs/05-API.md：BasePath /api（业务）、/auth（免登）、/internal 与 /healthz /readyz（内部运维）。
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/labstack/echo/v4"
)

// Envelope 统一响应包裹（接口约定 §2）：code=0 表示成功。
type Envelope struct {
	Code    int    `json:"code"`
	Data    any    `json:"data"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
}

// 错误码（docs/05-API.md §2.1）。
const (
	codeOK           = 0
	codeBadRequest   = 40000
	codeUnauthorized = 40100
	codeRoleMapped   = 40101
	codeForbidden    = 40300
	codeRowForbidden = 40301
	codeNotFound     = 40400
	codeConflict     = 40900
	codeReadOnly     = 40901
	codeRateLimited  = 42900
	codeInternal     = 50000
	codeNotReady     = 50300
)

const (
	ctxKeyTraceID = "trace_id"
	ctxKeySession = "jx_session"
)

func newTraceID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "trace-unavailable"
	}
	return hex.EncodeToString(buf)
}

func traceID(c echo.Context) string {
	if v, ok := c.Get(ctxKeyTraceID).(string); ok {
		return v
	}
	return ""
}

// ok 返回成功包裹。
func ok(c echo.Context, data any) error {
	return c.JSON(http.StatusOK, Envelope{Code: codeOK, Data: data, Message: "ok", TraceID: traceID(c)})
}

// fail 返回错误包裹。
func fail(c echo.Context, status, code int, msg string) error {
	return c.JSON(status, Envelope{Code: code, Data: nil, Message: msg, TraceID: traceID(c)})
}

// failWithDetail 返回带结构化明细的错误包裹（Data=detail）。
// 用途：链算不到人时的 `unresolved_roles`（N-018 过渡口径：40000 + 明细）等
// 「错误消息承载不了结构、但客户端需要机器可读」的场景。
func failWithDetail(c echo.Context, status, code int, msg string, detail map[string]any) error {
	return c.JSON(status, Envelope{Code: code, Data: detail, Message: msg, TraceID: traceID(c)})
}
