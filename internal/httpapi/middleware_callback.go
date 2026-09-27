package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/labstack/echo/v4"
)

// middleware_callback.go —— 回调报文留痕中间件（docs/16 §2-E / G-7）。
//
// ★ **只挂** `POST /approval/external/callback` **一条路由**（router.go 内以路由级
// MiddlewareFunc 追加），**不全局生效**——其他路由（台账/附件/凭据等敏感面）的 body
// 一律不落日志（最小化；改 logMiddleware 全局记 body 属被否决的替代方案）。
//
// ★ 读取与回填：`io.ReadAll` 读 body 后必须用 `bytes.NewReader` **回填**
// `c.Request().Body`，否则下游 handler 读不到（Echo 不会自动重放）。
//
// ★ 脱敏（docs/16 §2-E，逐条）：
//   - `token`：**全打码**（绝不落明文，记 `token=<len:N>`）；
//   - `reason`：截断至 200 字符（审批意见可能含敏感文本）；
//   - `attachments`：只记条数；
//   - 其余字段（action_type / user_id / approval_code / instance_id / task_id /
//     message_id / id / encrypt / action_context）：原样落（action_context 含 biz_no，
//     非敏感、排障必需）。
//
// ★ 留痕上限 **64KB**（对齐 docs/14 runbook 的 body ≤ 64KB 门禁）：脱敏后的留痕视图
// 超限即截断并标 `[truncated]`（截断作用于**日志视图**，下游拿到的仍是完整 body）。
// 日志键：trace_id（与 logMiddleware 同源）＋ biz_no ＋ message_id ＋ status。

// callbackBodyLogMaxBytes 回调留痕视图上限（64KB，docs/16 §2-E）。
const callbackBodyLogMaxBytes = 64 * 1024

// callbackBodyLog 回调路由级中间件：读 body → 回填 → 处理后落**脱敏**留痕。
func (d Deps) callbackBodyLog(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		raw, err := io.ReadAll(c.Request().Body)
		if err != nil {
			// 读不出 body：下游同样读不出，直接交给 handler 报 400（不吞错误）。
			return next(c)
		}
		// ★ 回填 body（否则下游 json.Decoder 读到 EOF，全链路 400）。
		c.Request().Body = io.NopCloser(bytes.NewReader(raw))

		// 先处理、后落痕（需要拿响应 status）。
		err = next(c)

		// ★ 脱敏视图：token 全打码 / reason 截 200 / attachments 只记条数，其余原样落。
		var body extCallbackBody
		if uErr := json.Unmarshal(raw, &body); uErr != nil {
			// 报文不是合法 JSON：各字段为零值、仍按脱敏形态落痕；400 语义由下游 handler 给出。
			d.Log.Debug("回调留痕：body 解析失败（按零值脱敏落痕）", "trace_id", traceID(c), "error", uErr.Error())
		}
		var inner callbackACInner
		if ac := strings.TrimSpace(body.ActionContext); strings.HasPrefix(ac, "{") {
			if iErr := json.Unmarshal([]byte(ac), &inner); iErr != nil {
				d.Log.Debug("回调留痕：action_context 解析失败（biz_no 走反解兜底）",
					"trace_id", traceID(c), "error", iErr.Error())
			}
		}
		view := map[string]any{
			"action_type":    body.ActionType,
			"user_id":        body.UserID,
			"approval_code":  body.ApprovalCode,
			"instance_id":    body.InstanceID,
			"task_id":        body.TaskID,
			"message_id":     body.MessageID,
			"id":             body.ID,
			"encrypt":        body.Encrypt,
			"action_context": body.ActionContext,
			"reason":         truncateRunes(body.Reason, 200),
			"attachments":    len(body.Attachments), // 只记条数
			// ★★ token 全打码：绝不落明文（action_callback_token 是回调真实性凭据）。
			"token": fmt.Sprintf("token=<len:%d>", len(body.Token)),
		}
		viewRaw, mErr := json.Marshal(view)
		truncated := false
		if mErr == nil && len(viewRaw) > callbackBodyLogMaxBytes {
			viewRaw = append(viewRaw[:callbackBodyLogMaxBytes], []byte("[truncated]")...)
			truncated = true
		}

		// biz_no：与 handler 同一解析口径（action_context 主读 → instance_id 反解 → 顶层兜底），
		// 供按单检索（docs/14 runbook 的 `grep <biz_no>` 排障入口）。
		bizNo := resolveCallbackBizNo(body, inner)
		logArgs := []any{
			"trace_id", traceID(c),
			"biz_no", bizNo,
			"message_id", body.MessageID,
			"status", c.Response().Status,
			"body_bytes", len(raw),
		}
		if truncated {
			logArgs = append(logArgs, "note", "留痕视图超 64KB 已截断")
		}
		if mErr == nil {
			logArgs = append(logArgs, "body", string(viewRaw))
		} else {
			logArgs = append(logArgs, "body", "<marshal-failed>")
		}
		// ★★ 原始报文留痕（2026-09-28 加）：**脱敏视图看不到"我方未解析的字段"**，
		//   曾在联调中因此无法定位「平台实发报文与我方复现报文 body_bytes 不同（187 vs 154）
		//   却看不出差在哪、导致真实点击恒 400」的问题。此处额外落**原始 body**
		//   （token 值仍打码、超限截断），以便直接复现平台请求。
		logArgs = append(logArgs, "raw_body", maskRawToken(raw, body.Token))
		if err != nil {
			// handler 返回错误（echo  errorHandler 已写响应）：留痕不得吞掉原错误。
			logArgs = append(logArgs, "handler_error", err.Error())
			d.Log.Warn("回调报文留痕", logArgs...)
			return err
		}
		d.Log.Info("回调报文留痕", logArgs...)
		return nil
	}
}

// resolveCallbackBizNo 从报文（含已解出的 action_context 内 JSON）解出 biz_no，
// 供留痕检索；口径与 handler 三级读法一致：action_context 主读 → instance_id 反解 → 顶层兜底。
func resolveCallbackBizNo(body extCallbackBody, inner callbackACInner) string {
	bizNo := strings.TrimSpace(inner.BizNo)
	if bizNo == "" {
		bizNo = strings.TrimSpace(bizNoFromInstanceID(firstNonEmptyStr(body.InstanceID, body.InstanceCode)))
	}
	if bizNo == "" {
		bizNo = strings.TrimSpace(body.BizNo)
	}
	return bizNo
}

// truncateRunes 按字符数截断（reason 脱敏用；中文按 rune 计，不腰斩 UTF-8）。
func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// rawBodyLogMaxBytes 原始报文留痕上限（够看结构即可，避免日志爆量）。
const rawBodyLogMaxBytes = 4096

// maskRawToken 生成**可安全落日志的原始报文**：把 token 的**值**替换为长度指纹，
// 其余字节原样保留（含平台特有的、我方结构体未覆盖的字段——这正是本函数存在的理由）。
// ★ 绝不落 token 明文（action_callback_token 是回调真实性凭据）。
func maskRawToken(raw []byte, token string) string {
	out := string(raw)
	if t := strings.TrimSpace(token); t != "" {
		// token 值在 JSON 里作为字符串出现，整体替换（含可能的转义形态）。
		out = strings.ReplaceAll(out, t, fmt.Sprintf("<len:%d>", len(t)))
	}
	return truncateRunes(out, rawBodyLogMaxBytes)
}
