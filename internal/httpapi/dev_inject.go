package httpapi

import (
	"io"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// handleInjectEvent 开发模式事件注入端点（★ 仅当 DEV_MODE=true 时注册）。
//
// 用途：在 approval_code（Q1）与飞书凭据尚未到位时，提供不依赖飞书的端到端验证路径。
// 行为：接收一条模拟事件 JSON（2.0 版含 header.event_id 或 1.0 版含顶层 uuid），
// 走与长连接完全相同的同步段（inbox：解析幂等键 → 写收件箱 + 建待处理作业 → 立即返回）。
// 后续由 worker 拉详情 → 落库（可由运维端点或测试驱动 worker 处理）。
func (d Deps) handleInjectEvent(c echo.Context) error {
	payload, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "读取请求体失败")
	}
	if len(payload) == 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体为空")
	}

	res, err := d.Inbox.Handle(c.Request().Context(), payload)
	if err != nil {
		// 缺少幂等键 → 拒绝入库并告警（HTTP 400 / code 40000）。
		return fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
	}

	d.audit(c.Request().Context(), &store.AuditLogRow{
		Action: "dev_inject", Resource: "event", TargetID: res.Event.IdemKey, Result: "allow",
	})

	return ok(c, map[string]any{
		"idem_key":       res.Event.IdemKey,
		"schema_version": res.Event.SchemaVersion,
		"instance_code":  res.Event.InstanceCode,
		"inbox_id":       res.InboxID,
		"duplicate":      res.Duplicate,
		"job_created":    res.JobCreated,
		"duration_ms":    res.DurationMS,
	})
}
