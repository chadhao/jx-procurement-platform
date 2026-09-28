package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"bytes"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// codeGone 41000 —— 「入口已显式退役」（HTTP 410 Gone）。
//
// ★ 就地定义（不复用/改动 `response.go` 的集中错误码表）：本批次对 internal/httpapi 的
//
//	授权仅限本文件。
const codeGone = 41000

// handleHealthz 存活探针 + 启动自检四项快照 + 通讯录同步观测段（org_sync，非门禁）。
func (d Deps) handleHealthz(c echo.Context) error {
	return ok(c, map[string]any{
		"alive":      true,
		"version":    d.Version,
		"started_at": d.Health.StartedAt().Format(time.RFC3339),
		"checks":     d.Health.Checks(),
		// ★ 通讯录镜像同步状态（docs/08 §4.5）：只观测、**不参与** /readyz 门禁——
		//   外部依赖（飞书）故障不得放大成本地宕机。
		"org_sync": d.Health.OrgSync(),
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
	// ★ 空 body 合法（=订阅全部）；**格式错误必须 400**，否则会静默升级为"订阅全部"。
	if _, err := decodeOptionalBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法（approval_codes 需为字符串数组）: "+err.Error())
	}

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

// handleReconcile ★ 本入口已**显式退役**（HTTP 410 Gone）。
//
// ③ 下审批对账职能**迁至 `POST /internal/approval/check`**（唯一入口，R24）；本路由
// `POST /internal/sync/reconcile` 收敛为「通讯录侧」，其最终形态待 `docs/08` 实施。
//
// ★ 为什么"显式退役（410 + 指明权威入口）"，而不是删路由 / 加 nil 守卫：
//
//	· 删路由        → 调用方收到 `404`，看不出"曾经存在、现已迁移"，也拿不到新入口；
//	· nil 守卫 501/200 → 留下一个"看起来还能用"的**假入口**（B17 教训）；
//	· 空指针 `500`  → 语义是"服务器坏了"，而真实语义是"**入口已退役**" —— 不可诊断，
//	  与 `R18`/`P23`「看起来能跑但语义已错」同族。
//	故改为：**410 Gone + 明确 body 指明权威入口**（错误调用得到确定的、可诊断的拒绝）。
//
// ★ 本入口不再解析请求体、也不再触发任何对账（旧 `Reconciler` 已随 R23 退役）。
func (d Deps) handleReconcile(c echo.Context) error {
	return fail(c, http.StatusGone, codeGone,
		"该入口已退役：审批对账唯一入口为 POST /internal/approval/check"+
			"（本路由收敛为通讯录侧，待 docs/08 实施）")
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

// decodeOptionalBody 解析**可选**请求体：空 body 视为"未提供"（不报错），
// 但**格式错误必须报错**。
//
// ★ 为什么不能沿用 `_ = decodeBody(c, &req)`（静默审计 C6）：
// `decodeBody` 对空 body 返回 nil，对**非法 JSON 也返回 error** —— 调用方把它丢掉之后，
// 两种情形变得无法区分，非法请求会被当成"全都不填"，进而**把语义静默升级**为
// 「对全部 approval_code 执行」（多消耗飞书 API 配额、触发全量对账，且无人察觉）。
func decodeOptionalBody(c echo.Context, dst any) (provided bool, err error) {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return true, err
	}
	return true, nil
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
