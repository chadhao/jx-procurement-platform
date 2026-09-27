package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// handlers_approval.go —— 审批（转向 ③）接入层薄壳（T04b）。
//
// ★ 只做「解析请求 → 调 flow 核心 → 回包」：**业务逻辑一律在 `internal/flow/*.go`**，
//
//	本文件不得复制状态机语义（否则"回调 / 页面两键"会各成一套口径，docs/11 R11）。
//
// ★ 路由归属见 router.go：
//
//	· 入站回调 `POST /approval/external/callback` —— **挂 root `e`、绕开会话中间件**（R14）；
//	· 页面两键 / 四操作 / 待办 —— `api` 组（requireSession）。
//
// 说明：`40901` 为 docs/05-API.md §2.1 / §3.13 约定的「审批定义缺失 / 非本人任务 / 非 PENDING」
// 语义码；与 `response.go` 的 `codeReadOnly` 同值但语义不同，故**就地以专用常量**表达，
// 集中错误码表的合并另行排期（避免本批越界改动 response.go）。
const codeApprovalConflict = 40901

// ---------- 入站回调（★ 独立入站面，绕开会话中间件）----------

// extCallbackBody 飞书三方审批回调报文（字段名待 Q17 实采校准）。
type extCallbackBody struct {
	Token         string `json:"token"`
	ActionName    string `json:"action_name"` // APPROVE / REJECT
	ActionType    string `json:"action_type"` // 兼容字段
	BizNo         string `json:"biz_no"`
	InstanceCode  string `json:"instance_code"`
	TaskID        string `json:"task_id"`
	OpenID        string `json:"open_id"`
	Reason        string `json:"reason"`
	ActionContext string `json:"action_context"` // 可能为 JSON 字符串（内含定位字段）
	Operator      struct {
		OpenID string `json:"open_id"`
	} `json:"operator"`
}

// handleExternalApprovalCallback 处理飞书「同意 / 拒绝」回调（仅两键；四操作不在回调内）。
//
// ★ 响应纪律（docs/05-API §3.14）：**落盘即 200**；非法 token → **403**（不返回 401，
//
//	避免把会话语义暴露给飞书）；报文非法 → 400。状态机推进在 `flow.HandleCallback` 内
//	经其 Advancer 端口完成（本处理器不感知推进细节）。
func (d Deps) handleExternalApprovalCallback(c echo.Context) error {
	if d.Flow == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "审批服务未装配")
	}
	var body extCallbackBody
	if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "回调报文非法: "+err.Error())
	}
	// action_context 兼容：若为 JSON 字符串，则并入定位字段（缺字段时取之）。
	if ac := strings.TrimSpace(body.ActionContext); strings.HasPrefix(ac, "{") {
		var inner extCallbackBody
		if err := json.Unmarshal([]byte(ac), &inner); err == nil {
			body.BizNo = firstNonEmptyStr(body.BizNo, inner.BizNo)
			body.InstanceCode = firstNonEmptyStr(body.InstanceCode, inner.InstanceCode)
			body.TaskID = firstNonEmptyStr(body.TaskID, inner.TaskID)
			body.OpenID = firstNonEmptyStr(body.OpenID, inner.OpenID, inner.Operator.OpenID)
		}
	}
	req := flow.CallbackRequest{
		Token:          body.Token,
		BizNo:          strings.TrimSpace(body.BizNo),
		TaskID:         strings.TrimSpace(body.TaskID),
		OpType:         strings.ToUpper(firstNonEmptyStr(body.ActionName, body.ActionType)),
		OperatorOpenID: firstNonEmptyStr(body.OpenID, body.Operator.OpenID),
		Reason:         body.Reason,
	}
	res, err := d.Flow.HandleCallback(c.Request().Context(), req)
	if err != nil {
		if errors.Is(err, flow.ErrInvalidToken) {
			// ★ 非法 token：拒绝 + 告警（审计留痕），且**不返回 401**。
			d.audit(c.Request().Context(), &store.AuditLogRow{
				Action: "callback", Resource: "approval", TargetID: req.BizNo,
				Result: "deny", DetailJSON: `{"reason":"invalid_token"}`,
			})
			return fail(c, http.StatusForbidden, codeForbidden, "回调 token 校验失败")
		}
		if errors.Is(err, flow.ErrInvalidSubmit) || errors.Is(err, flow.ErrIllegalTransition) {
			return fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return ok(c, map[string]any{"accepted": res.Accepted, "duplicate": res.Duplicate})
}

// ---------- 页面两键（approve / reject）----------

// approvalActionBody 两键 / 四操作共用请求体。
type approvalActionBody struct {
	TaskID     string `json:"task_id"`
	Opinion    string `json:"opinion"`     // 两键意见
	Reason     string `json:"reason"`      // 四操作原因（与 opinion 兼容）
	Target     string `json:"target"`      // 转交 / 加签目标 open_id
	TargetName string `json:"target_name"` // 转交 / 加签目标姓名（展示）
	TargetNode string `json:"target_node"` // 回退目标 node_id
	Timing     string `json:"timing"`      // 加签时机 AFTER / BEFORE（默认 AFTER）
}

// handleApprovalApprove 我方页面「同意」（与回调同一状态机出口）。
func (d Deps) handleApprovalApprove(c echo.Context) error { return d.approveReject(c, flow.OpApprove) }

// handleApprovalReject 我方页面「拒绝」。
func (d Deps) handleApprovalReject(c echo.Context) error { return d.approveReject(c, flow.OpReject) }

func (d Deps) approveReject(c echo.Context, op string) error {
	if d.Flow == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "审批服务未装配")
	}
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	var body approvalActionBody
	if _, err := decodeOptionalBody(c, &body); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	bizNo := c.Param("biz_no")
	reason := firstNonEmptyStr(body.Opinion, body.Reason)
	ctx := c.Request().Context()
	if op == flow.OpApprove {
		err = d.Flow.Approve(ctx, bizNo, body.TaskID, idn.OpenID, reason)
	} else {
		err = d.Flow.Reject(ctx, bizNo, body.TaskID, idn.OpenID, reason)
	}
	if err != nil {
		return d.approvalError(c, err)
	}
	action := "approve"
	if op == flow.OpReject {
		action = "reject"
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role,
		Action: action, Resource: "approval", TargetID: bizNo, Result: "allow"})
	return ok(c, map[string]any{"biz_no": bizNo, "action": action})
}

// ---------- 四操作（transfer / addsign / rollback / cancel）----------

// handleApprovalTransfer 转交（当前任务审批人本人）。
func (d Deps) handleApprovalTransfer(c echo.Context) error {
	return d.simpleOp(c, flow.OpTransfer)
}

// handleApprovalAddSign 加签（timing ∈ {AFTER, BEFORE}，缺省 AFTER）。
func (d Deps) handleApprovalAddSign(c echo.Context) error {
	return d.simpleOp(c, flow.OpAddSign)
}

// handleApprovalRollback 回退（到更早节点）。
func (d Deps) handleApprovalRollback(c echo.Context) error {
	return d.simpleOp(c, flow.OpRollback)
}

// handleApprovalCancel 撤回（仅发起人本人）。
func (d Deps) handleApprovalCancel(c echo.Context) error {
	return d.simpleOp(c, flow.OpCancel)
}

// simpleOp 四操作共用骨架：解析 → 调 flow → 回包（业务规则全在 flow）。
func (d Deps) simpleOp(c echo.Context, op string) error {
	if d.Flow == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "审批服务未装配")
	}
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	var body approvalActionBody
	if _, err := decodeOptionalBody(c, &body); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	bizNo := c.Param("biz_no")
	reason := firstNonEmptyStr(body.Reason, body.Opinion)
	ctx := c.Request().Context()

	switch op {
	case flow.OpTransfer:
		err = d.Flow.Transfer(ctx, bizNo, body.TaskID, idn.OpenID, body.Target, body.TargetName, reason)
	case flow.OpAddSign:
		timing := flow.AddSignTiming(strings.ToUpper(strings.TrimSpace(body.Timing)))
		if timing == "" {
			timing = flow.AddSignAfter // ★ 缺省＝后置（保持既有调用方语义）
		}
		err = d.Flow.AddSign(ctx, bizNo, body.TaskID, idn.OpenID, body.Target, body.TargetName, reason, timing)
	case flow.OpRollback:
		err = d.Flow.Rollback(ctx, bizNo, idn.OpenID, body.TargetNode, reason)
	case flow.OpCancel:
		err = d.Flow.Cancel(ctx, bizNo, idn.OpenID, reason)
	default:
		return fail(c, http.StatusBadRequest, codeBadRequest, "未知操作: "+op)
	}
	if err != nil {
		return d.approvalError(c, err)
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role,
		Action: strings.ToLower(op), Resource: "approval", TargetID: bizNo, Result: "allow"})
	return ok(c, map[string]any{"biz_no": bizNo, "action": strings.ToLower(op)})
}

// ---------- 我的待办 ----------

// handleApprovalTasks 我的待办：`t_flow_task`（RELEASED ∧ PENDING ∧ assignee=me）。
//
// ★ 不变量（04a §2.3）：任一**非终态实例**的此类任务**恰 1 个**（顺序会签）。
func (d Deps) handleApprovalTasks(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	page, size, offset := pageParams(c)

	instances, _, err := d.DB.ListInstances(ctx, store.InstanceFilter{
		Status: flow.InstancePending, Limit: size, Offset: offset,
	})
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items := make([]map[string]any, 0)
	for _, inst := range instances {
		tasks, err := d.DB.ListFlowTasks(ctx, inst.BizNo)
		if err != nil {
			return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		}
		for _, t := range tasks {
			if t.AssigneeOpenID != idn.OpenID {
				continue
			}
			if t.Status != flow.TaskPending || t.ReleaseState != flow.ReleaseReleased {
				continue
			}
			item := map[string]any{
				"biz_no": inst.BizNo, "doc_type": inst.DocType, "node_name": t.NodeName,
				"task_id": t.TaskID, "task_order": t.TaskOrder,
				"applicant": inst.ApplicantOpenID, "status": inst.Status,
			}
			if inst.AmountCents != nil {
				item["amount_cents"] = *inst.AmountCents
				item["amount_display"] = formatCents(*inst.AmountCents)
			}
			items = append(items, item)
		}
	}
	return ok(c, map[string]any{"items": items, "page": page, "page_size": size})
}

// ---------- 内部对账入口（★ 审批对账唯一入口，R24）----------

// handleApprovalCheck 手动触发审批对账（对 `external_instances/check` 的 diff 判方向后重推）。
//
// ★ 鉴权＝`internal` 组（`X-Internal-Token` 或回环），见 router.go / docs/05-API §3.8。
// ★ 与旧 `/internal/sync/reconcile`（410 Gone）区分：本入口**不做补拉**，只判方向 + 重推。
func (d Deps) handleApprovalCheck(c echo.Context) error {
	if d.ApprovalReconciler == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "审批对账器未装配")
	}
	rep, err := d.ApprovalReconciler.RunOnce(c.Request().Context(), c.QueryParam("approval_code"))
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	if !rep.Configured {
		return c.JSON(http.StatusServiceUnavailable, Envelope{
			Code: codeNotReady, Data: map[string]any{"configured": false},
			Message: "对账外部 check 端口未配置", TraceID: traceID(c),
		})
	}
	return ok(c, map[string]any{
		"checked": rep.Checked, "missing": rep.Missing, "repushed": rep.Repushed,
		"stale_skip": rep.StaleSkip, "undecidable": rep.Undecidable, "alerts": rep.Alerts,
	})
}

// ---------- 错误映射 ----------

// approvalError 把 flow 的领域错误映射为 HTTP 语义（docs/05-API §3.13 错误码口径）。
func (d Deps) approvalError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fail(c, http.StatusNotFound, codeNotFound, "实例或任务不存在")
	case errors.Is(err, flow.ErrNotAssignee):
		return fail(c, http.StatusForbidden, codeRowForbidden, err.Error())
	case errors.Is(err, flow.ErrDefinitionMissing):
		return fail(c, http.StatusConflict, codeApprovalConflict, err.Error())
	case errors.Is(err, flow.ErrIllegalTransition), errors.Is(err, flow.ErrTaskHeld),
		errors.Is(err, flow.ErrNodeNotReached):
		return fail(c, http.StatusConflict, codeApprovalConflict, err.Error())
	case errors.Is(err, flow.ErrInvalidSubmit), errors.Is(err, flow.ErrInvalidToken):
		return fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
	default:
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
}
