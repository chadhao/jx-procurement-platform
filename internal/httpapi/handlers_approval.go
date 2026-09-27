package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
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
		InstanceCode:   strings.TrimSpace(body.InstanceCode), // ★ 下传报文 instance_id（口径 {app_id}:{biz_no}）→ flow 侧「报文与 biz_no 不一致即拒」
		TaskID:         strings.TrimSpace(body.TaskID),
		OpType:         strings.ToUpper(firstNonEmptyStr(body.ActionName, body.ActionType)),
		OperatorOpenID: firstNonEmptyStr(body.OpenID, body.Operator.OpenID),
		Reason:         body.Reason,
	}
	res, err := d.Flow.HandleCallback(c.Request().Context(), req)
	if err != nil {
		// ★★ #69 ①：**已落盘（Accepted）即 200** —— 只要 op_log 已写入（或幂等命中），请求即**已受理**；
		//   此后 advancer 的推进成败**不得**决定 HTTP 码（用户口径 D2：「先落盘，落盘成功就返回 200，
		//   然后慢慢跑业务」，D2 亦明言「不存在超时问题这就够了」）。把 err **记日志**
		//   （含定位键 biz_no/task_id/op_type/round）以便观测、**绝不静默**。
		//   ★ 真正的重驱动由 ② 的派生式修复循环（`flow.Service.RepairPendingApprovals`）兜底：
		//     它扫「op_log 有 APPROVE/REJECT 留痕、但任务仍 PENDING（同 round、已释放、实例非终态）」
		//     的行重驱动之（`act` 幂等，连跑不双推进）。
		//   ★★ 缺 ② 则 ① 会把**可见失败**变成**静默卡死**：落盘后回 200 → 飞书不再重试 →
		//     而幂等键已占（重发回调也判 Duplicate）→ 该任务**永不重推**。故 ①② 必须同批。
		if res.Accepted {
			d.Log.ErrorContext(c.Request().Context(),
				"审批回调已受理但异步推进失败（已回 200；由派生式修复循环重驱动）",
				"biz_no", req.BizNo, "task_id", req.TaskID, "op_type", req.OpType,
				"round", res.Round, "duplicate", res.Duplicate, "error", err.Error())
			return ok(c, map[string]any{
				"accepted": res.Accepted, "duplicate": res.Duplicate, "advance_deferred": true,
			})
		}
		// ★ 未受理：**枚举式**错误→状态码映射（定案 #60：枚举优于逐例；见 callbackErrorStatus，
		//   表见 docs/05-API §3.14）。未分类才落 500，绝不让已知 4xx 落 500。
		if errors.Is(err, flow.ErrInvalidToken) {
			// ★ 非法 token：拒绝 + 告警（审计留痕），且**不返回 401**（不把会话语义暴露给飞书）。
			d.audit(c.Request().Context(), &store.AuditLogRow{
				Action: "callback", Resource: "approval", TargetID: req.BizNo,
				Result: "deny", DetailJSON: `{"reason":"invalid_token"}`,
			})
			return fail(c, http.StatusForbidden, codeForbidden, "回调 token 校验失败")
		}
		status, code := callbackErrorStatus(err)
		return fail(c, status, code, err.Error())
	}
	return ok(c, map[string]any{"accepted": res.Accepted, "duplicate": res.Duplicate})
}

// callbackErrorStatus 把回调路径的领域错误**枚举式**映射为 HTTP 状态码 + 业务码。
//
// ★ **仅适用于「未受理」**（`CallbackResult.Accepted==false`）：已受理（落盘/幂等命中）一律 200，
// 不进入本表（见 handler 中的 #69 ① 分支）。
//
// 规则（team-lead 裁定）：① 客户端可纠正 → 4xx；② 服务端故障 → 5xx；③ 已受理（幂等命中）→ 200（不在此）。
// 表（见 docs/05-API §3.14）：
//
//	ErrInvalidToken      → 403 / codeForbidden       （报文不真实）
//	ErrNotAssignee       → 403 / codeRowForbidden    （非本人回调）
//	ErrInvalidSubmit     → 400 / codeBadRequest      （缺字段 / biz_no·task_id 不存在 / 防串单）
//	ErrIllegalTransition → 409 / codeApprovalConflict（非法操作 / 任务与实例不匹配）
//	ErrTaskHeld          → 409 / codeApprovalConflict（顺序会签未轮到）
//	ErrNodeNotReached    → 409 / codeApprovalConflict（节点尚未到达）
//	ErrDefinitionMissing → 409 / codeApprovalConflict（定义未注册）
//	（其他 / 未分类）      → 500 / codeInternal
//
// ★ 与页面路径 `approvalError` 的错误→码口径**一致**（同域错误同 HTTP 语义），
// 差异仅在回调额外把 `ErrInvalidToken` 归 403（页面路径无 token 维度）。
func callbackErrorStatus(err error) (status int, code int) {
	switch {
	case errors.Is(err, flow.ErrInvalidToken):
		return http.StatusForbidden, codeForbidden
	case errors.Is(err, flow.ErrNotAssignee):
		return http.StatusForbidden, codeRowForbidden
	case errors.Is(err, flow.ErrInvalidSubmit):
		return http.StatusBadRequest, codeBadRequest
	case errors.Is(err, flow.ErrIllegalTransition),
		errors.Is(err, flow.ErrTaskHeld),
		errors.Is(err, flow.ErrNodeNotReached),
		errors.Is(err, flow.ErrDefinitionMissing):
		return http.StatusConflict, codeApprovalConflict
	default:
		return http.StatusInternalServerError, codeInternal
	}
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

// ---------- 提交 / 详情 / 定义（§3.13）----------

// approvalSubmitBody 提交请求体（§3.13：`doc_type` + 表单字段 + `department`/`contact` + 审批链）。
//
// ★ `nodes` ＝审批链：`flow.Submit` 必需（`validateSubmit` 要求非空，`01a §4`）；由我方页面按定义给出。
type approvalSubmitBody struct {
	DocType        string           `json:"doc_type"`
	ApprovalCode   string           `json:"approval_code"`
	PrevBizNo      string           `json:"prev_biz_no"` // 驳回/撤回重提时指向旧单号（因果链）
	Department     string           `json:"department"`
	Supplier       string           `json:"supplier"`
	AmountCents    *int64           `json:"amount_cents"`
	PurposeClassL1 string           `json:"purpose_class_l1"`
	PurposeClassL2 string           `json:"purpose_class_l2"`
	Fields         map[string]any   `json:"fields"` // 已映射表单字段（键＝规范 biz_field）
	Nodes          []approvalNodeIn `json:"nodes"`
}

type approvalNodeIn struct {
	NodeID    string           `json:"node_id"`
	NodeName  string           `json:"node_name"`
	Seq       int              `json:"seq"`
	Approvers []approvalUserIn `json:"approvers"`
}

type approvalUserIn struct {
	OpenID string `json:"open_id"`
	Name   string `json:"name"`
}

// handleApprovalSubmit 我方提交：生成编号 + 建实例 + 首推飞书（§3.13；`04a §3`）。
//
// ★ 薄壳：校验 / 建实例 / 落库语义全在 `flow.Submit`（本文件不复制状态机）。
func (d Deps) handleApprovalSubmit(c echo.Context) error {
	if d.Flow == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "审批服务未装配")
	}
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	var body approvalSubmitBody
	if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	nodes := make([]flow.NodeSpec, 0, len(body.Nodes))
	for _, n := range body.Nodes {
		approvers := make([]flow.Approver, 0, len(n.Approvers))
		for _, a := range n.Approvers {
			approvers = append(approvers, flow.Approver{OpenID: a.OpenID, Name: a.Name})
		}
		nodes = append(nodes, flow.NodeSpec{NodeID: n.NodeID, NodeName: n.NodeName, Seq: n.Seq, Approvers: approvers})
	}
	ctx := c.Request().Context()
	bizNo, err := d.Flow.Submit(ctx, flow.SubmitInput{
		DocType:         body.DocType,
		ApprovalCode:    body.ApprovalCode,
		PrevBizNo:       body.PrevBizNo,
		ApplicantOpenID: idn.OpenID,
		Department:      firstNonEmptyStr(body.Department, idn.Department),
		AmountCents:     body.AmountCents,
		PurposeClassL1:  body.PurposeClassL1,
		PurposeClassL2:  body.PurposeClassL2,
		Supplier:        body.Supplier,
		BizFields:       body.Fields,
		Nodes:           nodes,
	})
	if err != nil {
		return d.approvalError(c, err)
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role,
		Action: "submit", Resource: "approval", TargetID: bizNo, Result: "allow"})
	inst, err := d.DB.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return ok(c, map[string]any{"biz_no": bizNo, "instance_id": inst.InstanceCode, "status": inst.Status})
}

// handleApprovalInstance 单实例审批详情 + 时间线（§3.13）。
//
// ★ 时间线 ＝ `t_flow_op_log`（细粒度操作留痕）；鉴权＝免登会话 + **行级过滤**（越权 → 40301）。
func (d Deps) handleApprovalInstance(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	bizNo := c.Param("biz_no")
	inst, err := d.DB.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fail(c, http.StatusNotFound, codeNotFound, "实例不存在")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	tasks, err := d.DB.ListFlowTasks(ctx, bizNo)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	if !approvalVisibleTo(idn, inst, tasks) {
		return fail(c, http.StatusForbidden, codeRowForbidden, "无权访问该审批实例")
	}
	ops, err := d.DB.ListFlowOpLogs(ctx, bizNo)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return ok(c, map[string]any{
		"biz_no":        inst.BizNo,
		"instance_code": inst.InstanceCode,
		"doc_type":      inst.DocType,
		"status":        inst.Status,
		"applicant":     inst.ApplicantOpenID,
		"department":    inst.Department,
		"amount_cents":  inst.AmountCents,
		"created_at":    inst.CreatedAt,
		"updated_at":    inst.UpdatedAt,
		"tasks":         approvalTaskViews(tasks),
		"ops":           approvalOpViews(ops),
	})
}

// approvalVisibleTo 行级可见性：申请人本人 / 该实例任一任务审批人 / 系统管理员 → 可见。
func approvalVisibleTo(idn permission.Identity, inst *store.Instance, tasks []store.FlowTask) bool {
	if strings.EqualFold(strings.TrimSpace(idn.Role), roleSysAdmin) {
		return true
	}
	if strings.TrimSpace(inst.ApplicantOpenID) == idn.OpenID {
		return true
	}
	for _, t := range tasks {
		if t.AssigneeOpenID == idn.OpenID {
			return true
		}
	}
	return false
}

func approvalTaskViews(tasks []store.FlowTask) []map[string]any {
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, map[string]any{
			"task_id": t.TaskID, "node_id": t.NodeID, "node_name": t.NodeName,
			"task_order": t.TaskOrder, "round": t.Round, "assignee": t.AssigneeOpenID,
			"status": t.Status, "release_state": t.ReleaseState,
		})
	}
	return out
}

func approvalOpViews(ops []store.FlowOpLog) []map[string]any {
	out := make([]map[string]any, 0, len(ops))
	for _, o := range ops {
		out = append(out, map[string]any{
			"op_type": o.OpType, "node_id": o.NodeID, "task_id": o.TaskID,
			"actor": o.ActorOpenID, "from_status": o.FromStatus, "to_status": o.ToStatus,
			"reason": o.Reason, "created_at": o.CreatedAt,
		})
	}
	return out
}

// handleApprovalDefs 三方审批定义清单（`t_approval_def`；§3.13「可选管理页」）。
//
// ★ 鉴权＝系统管理员（`/api/admin` 域语义，`04a §3.5`）。
func (d Deps) handleApprovalDefs(c echo.Context) error {
	if _, ok := d.requireSysAdmin(c); !ok {
		return nil // requireSysAdmin 已写出 403/401 响应
	}
	defs, err := d.DB.ListApprovalDefs(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items := make([]map[string]any, 0, len(defs))
	for _, df := range defs {
		items = append(items, map[string]any{
			"approval_code": df.ApprovalCode,
			"doc_type":      df.DocType,
			"name":          df.Name,
			"group_name":    df.GroupName,
			"def_version":   df.DefVersion,
			"updated_at":    df.UpdatedAt,
		})
	}
	return ok(c, map[string]any{"items": items})
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
