package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// handleHealthz 存活探针 + 启动自检四项快照。
func (d Deps) handleHealthz(c echo.Context) error {
	return ok(c, map[string]any{
		"alive":      true,
		"version":    d.Version,
		"started_at": d.Health.StartedAt().Format(time.RFC3339),
		"checks":     d.Health.Checks(),
	})
}

// handleReadyz 就绪探针：自检四项 + 运行期指标（未就绪返回 50300）。
func (d Deps) handleReadyz(c echo.Context) error {
	ctx := c.Request().Context()
	ready := d.Health.Ready()

	queueDepth, _ := d.DB.CountJobsByState(ctx, "QUEUED")
	deadletters, _ := d.DB.CountDeadletters(ctx)

	subscribeStates, _ := d.DB.ListSubscribeStates(ctx)
	subs := make([]map[string]any, 0, len(subscribeStates))
	for _, s := range subscribeStates {
		subs = append(subs, map[string]any{
			"approval_code": s.ApprovalCode,
			"doc_type":      s.DocType,
			"subscribed":    s.Subscribed,
			"last_result":   s.LastResult,
			"last_error":    s.LastError,
		})
	}

	body := map[string]any{
		"ready":              ready,
		"checks":             d.Health.Checks(),
		"worker_queue_depth": queueDepth,
		"deadletter_total":   deadletters,
		"subscribe_states":   subs,
		"metrics":            d.Metrics.Snapshot(),
	}
	if !ready {
		return c.JSON(http.StatusServiceUnavailable, Envelope{Code: codeNotReady, Data: body, Message: "服务未就绪", TraceID: traceID(c)})
	}
	return ok(c, body)
}

// handleSubscribe 手动（重）订阅审批事件。
func (d Deps) handleSubscribe(c echo.Context) error {
	var req struct {
		ApprovalCodes []string `json:"approval_codes"`
	}
	_ = decodeBody(c, &req)

	var (
		results map[string]any
		failed  []string
	)
	if len(req.ApprovalCodes) == 0 {
		res, f := d.Subscriber.SubscribeAll(c.Request().Context())
		results = toResultMap(res)
		failed = f
	} else {
		res, f := d.Subscriber.Subscribe(c.Request().Context(), req.ApprovalCodes)
		results = toResultMap(res)
		failed = f
	}
	// 更新订阅自检快照。
	d.Health.SetSubscribe(len(failed) == 0 && len(results) > 0, failed)

	d.audit(c.Request().Context(), &store.AuditLogRow{Action: "subscribe", Resource: "approval", Result: resultOf(len(failed) == 0)})
	return ok(c, map[string]any{"results": results, "failed": failed})
}

// handleReconcile 手动触发对账补拉。
func (d Deps) handleReconcile(c echo.Context) error {
	var req struct {
		ApprovalCode string `json:"approval_code"`
		From         string `json:"from"`
		To           string `json:"to"`
	}
	_ = decodeBody(c, &req)

	from := parseTimeParam(req.From)
	to := parseTimeParam(req.To)
	missing, filled, err := d.Reconciler.Run(c.Request().Context(), req.ApprovalCode, from, to)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(c.Request().Context(), &store.AuditLogRow{Action: "reconcile", Resource: "approval", Result: "allow"})
	return ok(c, map[string]any{"missing": missing, "filled": filled})
}

// handleReplay 人工重放死信事件。
func (d Deps) handleReplay(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的事件/死信 id")
	}
	replayCount, err := d.Worker.ReplayDeadletter(c.Request().Context(), id)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "死信不存在或重放失败: "+err.Error())
	}
	d.audit(c.Request().Context(), &store.AuditLogRow{Action: "replay", Resource: "deadletter", TargetID: c.Param("id"), Result: "allow"})
	return ok(c, map[string]any{"replay_count": replayCount})
}

// ---------- 小工具 ----------

func decodeBody(c echo.Context, dst any) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil || len(body) == 0 {
		return err
	}
	return json.Unmarshal(body, dst)
}

func parseTimeParam(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func resultOf(okFlag bool) string {
	if okFlag {
		return "allow"
	}
	return "deny"
}

func toResultMap(in map[string]feishu.SubscribeResult) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
