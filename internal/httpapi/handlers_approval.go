package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/approval"
	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/config"
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

// flexID 兼容「平台发**数字**」与「发**字符串**」两种形态的 id 字段。
//
// ★★ 依据（2026-09-28 真机实测 ＋ 官方字段表）：
//   - 官方《三方快捷审批回调》字段表明确 **`message_id` 类型为 `int64`**；
//   - 实测平台发的正是**未加引号的数字**（`"message_id":7690275278685244603`）；
//   - 而我方调 `message/update` 时同一个值用**字符串**形态被平台接受（读写不对称）。
//
// ⇒ 若用 `string` 接收：Go 报 `json: cannot unmarshal number into Go value of type string`
// ⇒ **整个 Decode 失败** ⇒ 回调整体 400（`duration_ms: 0`、未做任何 IO）——这正是
// 2026-09-28「飞书真实点击恒 400、而我方用字符串手工复现却 200」的**唯一根因**。
// ⇒ `json.Number` 亦不可用（它只接受 JSON 数字、不接受字符串）⇒ 故自定义本类型。
//
// ★ 教训：**核对平台报文时，字段「类型」与字段「名」同等重要** ——
// 只对名字不对类型，会在解析层就失败，且错误现象离根因很远（表现为"报文非法"）。
type flexID string

// UnmarshalJSON 同时接受 JSON 数字、JSON 字符串与 null。
func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*f = ""
		return nil
	}
	*f = flexID(strings.Trim(s, `"`))
	return nil
}

// String 取字符串形态（落库 / 比较 / 日志用）。
func (f flexID) String() string { return string(f) }

// extCallbackBody 飞书三方审批回调报文。
//
// ★★ 字段名已按官方《三方快捷审批回调》2026-09-27 实测校准（docs/16 §2-A-1 / G-1；
//
//	实测铁证：官方格式打我方 ⇒ `回调缺少 biz_no/task_id`，旧自造格式才走到「无对应实例」）。
//	官方字段：action_type(必) / user_id(必) / approval_code(必) / token(必) / action_context /
//	instance_id / task_id / message_id(卡片操作必填) / id / reason / attachments / encrypt。
//	★★ 官方【不发】顶层 biz_no —— biz_no 的主读来源＝action_context 内 JSON
//	（推侧 BuildSnapshot 写入，docs/16 §2-B）；顶层 biz_no / instance_code / open_id /
//	action_name / operator.open_id **降为兼容读**、不再是主读字段（兼容窗口见下）。
//
// ★ 兼容窗口＝**一个发布版本**（docs/16 §2-A-2；本批随 V2.10 上线，**废弃时点＝下一个
//
//	发布版本切换时删除「兼容读」段**，届时须同步 05-API §3.14 与 16 §2-A-2 的口径）。
type extCallbackBody struct {
	// ---- 官方字段（主读）----
	ActionType    string            `json:"action_type"`    // APPROVE / REJECT（官方必填）
	UserID        string            `json:"user_id"`        // 操作人 user_id（租户内域，官方必填）
	ApprovalCode  string            `json:"approval_code"`  // 官方必填；双 code 池宽松校验用（docs/16 G-8）
	Token         string            `json:"token"`          // action_callback_token
	ActionContext string            `json:"action_context"` // 我方自定义上下文 JSON（推侧写入、期望原样回传；V-1 待实测）
	InstanceID    string            `json:"instance_id"`    // 我方口径 {app_id}:{biz_no}
	TaskID        string            `json:"task_id"`        // 列表操作必填
	MessageID     flexID            `json:"message_id"`     // 卡片消息 id（落 t_flow_op_log.message_id，0013）
	ID            string            `json:"id"`             // 官方报文 id（留痕用）
	Reason        string            `json:"reason"`         // 审批意见
	Attachments   []json.RawMessage `json:"attachments"`    // 附件（留痕只记条数；内容不解析）
	Encrypt       string            `json:"encrypt"`        // 加密标记（原样留痕）
	// ---- 兼容读（旧自造报文；兼容窗口＝一个发布版本，见上）----
	ActionName   string `json:"action_name"`   // 旧操作字段（→ action_type）
	BizNo        string `json:"biz_no"`        // ★ 官方不发；仅最后兜底（须打 warn）
	InstanceCode string `json:"instance_code"` // 旧实例标识字段（→ instance_id）
	OpenID       string `json:"open_id"`       // 旧操作人字段（同域，免转换）
	Operator     struct {
		OpenID string `json:"open_id"`
	} `json:"operator"`
}

// callbackACInner action_context 内 JSON 的解出形态（推侧 BuildSnapshot 写入
// `{"biz_no":…,"task_id":…}`；兼容期允许旧自造报文携带 open_id 等定位字段）。
type callbackACInner struct {
	BizNo    string `json:"biz_no"`
	TaskID   string `json:"task_id"`
	OpenID   string `json:"open_id"`
	Operator struct {
		OpenID string `json:"open_id"`
	} `json:"operator"`
}

// bizNoFromInstanceID 从我方 instance_id 反解 biz_no（docs/16 §2-A-2 兜底链）：
// 我方推的 instance_id ＝ `{app_id}:{biz_no}`（flow.Service.instanceCode，04a §3.3），
// 剥掉 `app_id:` 前缀即得；无冒号（appID 为空的开发/测试态＝裸单号）则原样返回。
func bizNoFromInstanceID(instanceID string) string {
	if i := strings.Index(instanceID, ":"); i >= 0 {
		return instanceID[i+1:]
	}
	return instanceID
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

	// ★ action_context 解析（docs/16 §2-B 解侧；与推侧 BuildSnapshot 同批约定）：
	//   以 `{` 开头 ⇒ 我方写入的 JSON（主读 biz_no；兼容期兼读 task_id / open_id）；
	//   否则（旧纯 task_id 字符串等）⇒ **忽略**、走 instance_id 反解兜底（§2-A-2）。
	ac := strings.TrimSpace(body.ActionContext)
	var inner callbackACInner
	if strings.HasPrefix(ac, "{") {
		if err := json.Unmarshal([]byte(ac), &inner); err != nil {
			// 回传上下文解析失败不致命（biz_no 还有 instance_id 反解兜底），但必须可见。
			d.Log.Warn("回调 action_context 解析失败（走 instance_id 反解兜底）",
				"trace_id", traceID(c), "error", err.Error())
		}
	}

	// ★ biz_no 三级读法（docs/16 §2-A-2）：
	//   ① 主读＝action_context 内 JSON 的 biz_no（官方不发顶层 biz_no）；
	//   ② 兜底＝从 instance_id 反解（我方 instance_id ＝ {app_id}:{biz_no}，剥前缀即得）；
	//   ③ 最后兜底＝顶层 biz_no（旧自造报文；★★ 官方不发该字段，命中必须打 warn——
	//     兼容窗口＝一个发布版本，废弃时点见 extCallbackBody 注释）。
	bizNo := strings.TrimSpace(firstNonEmptyStr(inner.BizNo))
	if bizNo == "" {
		instanceID := strings.TrimSpace(firstNonEmptyStr(body.InstanceID, body.InstanceCode))
		bizNo = strings.TrimSpace(bizNoFromInstanceID(instanceID))
	}
	if bizNo == "" && strings.TrimSpace(body.BizNo) != "" {
		bizNo = strings.TrimSpace(body.BizNo)
		d.Log.Warn("回调使用顶层 biz_no（官方《三方快捷审批回调》不发该字段；兼容窗口最后兜底，废弃时点见 extCallbackBody 注释）",
			"trace_id", traceID(c), "biz_no", bizNo)
	}
	// task_id：顶层主读；兼容期可读 action_context 内 JSON（docs/16 §2-A-2）。
	taskID := strings.TrimSpace(firstNonEmptyStr(body.TaskID, inner.TaskID))

	// ★ 操作人读法（docs/16 §2-A-2/A-3）：user_id 优先（租户内域，须**转换**）；
	//   无 user_id 时读 open_id（旧自造报文，同域、免转换）。
	// ★★ 红线（docs/16 §2-A-3，逐字执行）：绝不把 user_id 直接塞进 OperatorOpenID——
	//   user_id 与 open_id 不同域，拿去比 assignee_open_id 恒不命中 ⇒ 假 403；
	//   转换失败 ⇒ 可见拒绝（40000）＋告警日志，绝不静默放行。
	// ★ 操作人读法（docs/16 §2-A-2/A-3）：user_id 优先（租户内域，须**转换**）；
	//   无 user_id 时读 open_id（旧自造报文，同域、免转换；兼容候选＝报文顶层 / operator 对象 /
	//   action_context 内 JSON 三处）。
	operatorOpenID := strings.TrimSpace(
		firstNonEmptyStr(body.OpenID, body.Operator.OpenID, inner.OpenID, inner.Operator.OpenID))
	operatorUserID := strings.TrimSpace(body.UserID)
	if operatorUserID != "" {
		if d.Contact == nil {
			// 转换端口未装配＝装配缺陷，可见失败（503），绝不静默放行。
			return fail(c, http.StatusServiceUnavailable, codeNotReady,
				"回调操作人身份转换服务未装配（user_id 域无法鉴权）")
		}
		openID, err := d.Contact.GetOpenIDByUserID(c.Request().Context(), operatorUserID)
		if err != nil {
			// ★ 转换失败 ⇒ 可见拒绝 40000 ＋ 告警日志（不落 op_log、不占幂等键，定案 #62）。
			d.Log.Error("回调 user_id→open_id 转换失败（可见拒绝，不占幂等键）",
				"trace_id", traceID(c), "user_id", operatorUserID, "error", err.Error())
			return fail(c, http.StatusBadRequest, codeBadRequest,
				"回调操作人身份转换失败（user_id 域 → open_id 域）: "+err.Error())
		}
		operatorOpenID = strings.TrimSpace(openID)
	}

	req := flow.CallbackRequest{
		Token: body.Token,
		// ★ InstanceCode 下传报文 instance_id（官方字段；旧 instance_code 兼容）
		//   → flow 侧「报文与 biz_no 不一致即拒」（防串单）。
		InstanceCode: strings.TrimSpace(firstNonEmptyStr(body.InstanceID, body.InstanceCode)),
		BizNo:        bizNo,
		TaskID:       taskID,
		OpType:       strings.ToUpper(firstNonEmptyStr(body.ActionType, body.ActionName)),
		// ★★ user_id 恒经转换后才落 OperatorOpenID（见上红线注释）；兼容期旧报文
		//   的 open_id 同域直读。OperatorUserID 仅排障留痕、不参与鉴权（A-4）。
		OperatorOpenID: operatorOpenID,
		OperatorUserID: operatorUserID,
		ApprovalCode:   strings.TrimSpace(body.ApprovalCode),
		MessageID:      strings.TrimSpace(body.MessageID.String()),
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
	TaskID     string         `json:"task_id"`
	Opinion    string         `json:"opinion"`     // 两键意见
	Reason     string         `json:"reason"`      // 四操作原因（与 opinion 兼容）
	Target     string         `json:"target"`      // 转交 / 加签目标 open_id
	TargetName string         `json:"target_name"` // 转交 / 加签目标姓名（展示）
	TargetNode string         `json:"target_node"` // 回退目标 node_id
	Timing     string         `json:"timing"`      // 加签时机 AFTER / BEFORE（默认 AFTER）
	Fields     map[string]any `json:"fields"`      // 审批时点结构化填报（N-015：supervisor_approval 指定经办）
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
		// ★ 恒传非 nil（N-015 批 2）：我方页面通道执行必填；飞书回调/repair 走 nil 豁免。
		fields := body.Fields
		if fields == nil {
			fields = map[string]any{}
		}
		err = d.Flow.Approve(ctx, bizNo, body.TaskID, idn.OpenID, reason, fields)
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
	// ★ 解绿框（用户实测反馈 2026-09-28）：待办条目补 `assignee_name` / `assignee_department`
	//   （数据源 t_user_role，批量查询防 N+1；查不到 ⇒ 留空，绝不塞 open_id）。
	roleMap := map[string]store.UserRole{}
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
			if len(roleMap) == 0 {
				rm, err := d.DB.MapUserRolesByOpenIDs(ctx, []string{idn.OpenID})
				if err != nil {
					return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
				}
				roleMap = rm
			}
			item := map[string]any{
				"biz_no": inst.BizNo, "doc_type": inst.DocType, "node_name": t.NodeName,
				"task_id": t.TaskID, "task_order": t.TaskOrder,
				"applicant": inst.ApplicantOpenID, "status": inst.Status,
			}
			if r, ok := roleMap[t.AssigneeOpenID]; ok {
				item["assignee_name"] = r.Name
				item["assignee_department"] = r.Department
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

// approvalSubmitBody 提交请求体（§3.13；M4 契约收敛）。
//
// ★ `nodes` 字段**保留但一律拒绝**（d5：调用方不再决定链 —— 非空即 400 可见失败，
//
//	不静默忽略；旧契约迁移期零成本，因前端原无调用方）。
//
// ★ 审批链由服务端计算（chain.Service，spec/chain.json 唯一权威源，N-008）。
type approvalSubmitBody struct {
	DocType        string `json:"doc_type"`
	ApprovalCode   string `json:"approval_code"`
	PrevBizNo      string `json:"prev_biz_no"` // 驳回/撤回重提时指向旧单号（因果链）
	Department     string `json:"department"`
	Supplier       string `json:"supplier"`
	AmountCents    *int64 `json:"amount_cents"`
	PurposeClassL1 string `json:"purpose_class_l1"` // 兼容旧键；schema 权威键＝usage_category_l1
	PurposeClassL2 string `json:"purpose_class_l2"`
	// usage_category_*：spec/forms 的权威键（D8：与旧键双写皆可，服务端取并集）。
	UsageCategoryL1 string `json:"usage_category_l1"`
	UsageCategoryL2 string `json:"usage_category_l2"`
	// PaymentMethodInput 费用线按支付方式二分（enums#route_resolution）：
	//   personal_advance / corporate_direct。
	PaymentMethodInput string         `json:"payment_method_input"`
	Fields             map[string]any `json:"fields"` // 已映射表单字段（键＝规范 biz_field）
	// HasContract T3/R-26：该支出是否签了合同 —— 付款路径第 1 判据 + 合同两级触发。
	// CT 单据恒视为已签合同（未显式传也强制 true）。
	HasContract bool `json:"has_contract"`
	// AttachmentIDs 提交前上传的暂存附件 id（M6）：提交事务内绑定，任一不可绑定整体回滚。
	AttachmentIDs []string         `json:"attachment_ids"`
	Nodes         []approvalNodeIn `json:"nodes"` // ★ 仅用于检测并拒绝（d5）
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

// handleApprovalSubmit 我方提交（§3.13；M4 契约收敛）：
//
//	服务端算链 → 表单结构化校验 → 幂等（Idempotency-Key）→ flow.Submit（编号/落库/首推）。
//
// ★ 薄壳：状态机语义全在 `flow.Submit`；链与分档在 `chain.Service`（preview 共算，D5）。
func (d Deps) handleApprovalSubmit(c echo.Context) error {
	if d.Flow == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "审批服务未装配")
	}
	if d.Spec == nil || d.Chain == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "机读规格/链计算未装配")
	}
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	var body approvalSubmitBody
	if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	ctx := c.Request().Context()

	// ---- d5：调用方 nodes 一律拒绝（链由服务端计算）----
	if len(body.Nodes) > 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"审批链由服务端计算：请求体不再接受 nodes 字段（请移除后重试）")
	}
	// ---- approval_code ↔ doc_type 双向一致（M2-05；不一致=回调将静默失败，提交侧先行拦截）----
	if dt, ok := d.Maps.Approval.DocType(body.ApprovalCode); !ok || dt != body.DocType {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			fmt.Sprintf("approval_code %q 与 doc_type %q 不匹配或映射缺失", body.ApprovalCode, body.DocType))
	}

	// ---- 服务端算链（FR-M9-02）----
	usageL1 := firstNonEmptyStr(body.UsageCategoryL1, body.PurposeClassL1)
	usageL2 := firstNonEmptyStr(body.UsageCategoryL2, body.PurposeClassL2)
	// ★ N-039 P0：PR 定档依据服务端权威化 —— 汇总明细 → 与客户端 amount_cents 交叉校验
	//   （fail-closed）→ 定档与落库一律用服务端汇总值（低报降档路径闭环）。
	amountForTier := body.AmountCents
	if body.DocType == "PR" {
		estimated, perr := resolvePRAmountForTier(&body)
		if perr != nil {
			return fail(c, http.StatusBadRequest, codeBadRequest, perr.Error())
		}
		amountForTier = estimated
	}
	facts := chain.Facts{
		DocType:                  body.DocType,
		AmountCents:              amountForTier,
		UsageCategoryL1:          usageL1,
		PaymentMethodInput:       body.PaymentMethodInput,
		Department:               firstNonEmptyStr(body.Department, idn.Department),
		ApplicantIsOpsSupervisor: idn.Role == "综合运营主管",
		// N-013：forms/PR.json 已补 is_fixed_asset（申请人勾选）→ tier3_plus 的
		// "or is_fixed_asset" 分支自本字段接线起可达。
		IsFixedAsset: boolFromBodyField(body.Fields, "is_fixed_asset"),
		// T3 / R-26：CT 即有合同（强制 true）；其余按请求显式值。
		HasContract: body.HasContract || body.DocType == "CT",
	}
	// 付款路径（T3）：priority 1→4，值从 spec/chain.json#payment_route_rule 读。
	paymentRoute, pErr := chain.PaymentRouteOf(d.Spec, facts)
	if pErr != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, pErr.Error())
	}
	rc, err := d.Chain.Compute(ctx, facts)
	if err != nil {
		if isChainInputError(err) {
			return fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	// ---- 算不到人 ⇒ 阻断（FR-M9-02；N-018 裁定：专用错误码 40010 + unresolved_roles 明细）----
	if err := rc.EnsureResolvable(); err != nil {
		return failWithDetail(c, http.StatusBadRequest, codeChainUnresolved, err.Error(), map[string]any{
			"unresolved_roles": rc.Unresolved,
		})
	}

	// ---- 表单结构化校验（spec/forms schema；N-17 结构化子集）----
	form, hasForm := d.Spec.Forms[body.DocType]
	if hasForm {
		if verr := validateSubmitForm(form, mergeProvidedFields(&body, usageL1, usageL2)); verr != nil {
			return fail(c, http.StatusBadRequest, codeBadRequest, "表单校验失败: "+verr.Error())
		}
		// ---- T2：constant_ref 字段 —— 值必须在常量表 active 集合内；通过则写**值快照** ----
		//（policy.snapshot_rule：单据同时存 key 与显示值快照 ⇒ 字典改名/停用后历史单据一字不变）
		if verr := d.validateConstantRefs(ctx, form, body.Fields); verr != nil {
			return fail(c, http.StatusBadRequest, codeBadRequest, verr.Error())
		}
		// ---- T4：severity=hard 判据机判（amount_vs_pr / no_self_purchaser，R-25/R-27）----
		if verr := d.evaluateHardChecks(ctx, form, &body, idn.OpenID); verr != nil {
			return fail(c, http.StatusBadRequest, codeBadRequest, verr.Error())
		}
	}

	// ---- PC/SS 提交期系统字段注入（N-036 生产者；hash 前 ⇒ 指纹含注入值）----
	if verr := d.injectPCSSSystemFields(ctx, &body); verr != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, verr.Error())
	}

	// ---- 幂等（Idempotency-Key；d9 照 submission 模式）----
	idemKey := strings.TrimSpace(c.Request().Header.Get("Idempotency-Key"))
	idemHash := ""
	if idemKey != "" {
		if raw, mErr := json.Marshal(body); mErr == nil {
			sum := sha256.Sum256(raw)
			idemHash = hex.EncodeToString(sum[:])
		}
	}

	// ---- 提交时实时回源（FR-M9-17 / M5）：同步 2s 超时、失败告警放行、标记落 ext_json ----
	orgVerify := d.orgVerifyAtSubmit(ctx, idn.OpenID, facts.Department)

	bizNo, err := d.Flow.Submit(ctx, flow.SubmitInput{
		DocType:         body.DocType,
		ApprovalCode:    body.ApprovalCode,
		PrevBizNo:       body.PrevBizNo,
		ApplicantOpenID: idn.OpenID,
		Department:      firstNonEmptyStr(body.Department, idn.Department),
		AmountCents:     amountForTier, // N-039：PR＝服务端明细汇总值（其余单据同 body 值）
		PurposeClassL1:  usageL1,
		PurposeClassL2:  usageL2,
		Supplier:        body.Supplier,
		BizFields:       body.Fields,
		Nodes:           rc.Spec,
		IdemKey:         idemKey,
		IdemPayloadHash: idemHash,
		OrgVerify:       orgVerify,
		StagingIDs:      body.AttachmentIDs,
	})
	switch {
	case errors.Is(err, flow.ErrIdemReplay):
		// 同键同载荷 ⇒ 200 复用首次结果（bizNo 由 Submit 携带返回）。
		inst, gErr := d.DB.GetInstanceByBizNo(ctx, bizNo)
		if gErr != nil {
			return fail(c, http.StatusInternalServerError, codeInternal, gErr.Error())
		}
		return ok(c, map[string]any{"biz_no": bizNo, "instance_id": inst.InstanceCode,
			"status": inst.Status, "idempotent_replay": true, "payment_route": paymentRoute})
	case errors.Is(err, flow.ErrIdemConflict):
		return fail(c, http.StatusConflict, codeConflict,
			"Idempotency-Key 冲突：该键已用于另一次请求（请求载荷不一致）")
	case err != nil:
		return d.approvalError(c, err)
	}
	// N-027：提交后 hard 自检（contract_no_format —— 需生成后的 biz_no；失败=内部一致性破坏，可见 500）
	if hasForm {
		if verr := d.verifyPostSubmitHard(form, bizNo); verr != nil {
			return fail(c, http.StatusInternalServerError, codeInternal, verr.Error())
		}
		// QC#l07_inspection_conclusion_written（when=提交后）：写关联 GR 的 L07 行；
		// 失败可见（500 带 biz_no）——「不静默通过、不得只记日志」
		if hasFormCheck(form, "l07_inspection_conclusion_written") {
			if verr := d.verifyQCPostSubmitL07(ctx, &body, bizNo); verr != nil {
				return fail(c, http.StatusInternalServerError, codeInternal, verr.Error())
			}
		}
		// GR#ledger_l07_written（when=落账后；GR 无审批链提交即终态 ⇒ 落账先于自检）：
		// 失败可见（500 带 biz_no）—— 台账缺行/缺列 ≠ 没有验收
		if hasFormCheck(form, "ledger_l07_written") {
			if verr := d.verifyGRPostSubmitL07(ctx, bizNo); verr != nil {
				return fail(c, http.StatusInternalServerError, codeInternal, verr.Error())
			}
		}
		// SUB#ledger_l06_written（when=落账后，顺序纪律同上；只校验提交时能确定的
		// 存档 6 列 + 运营 2 列，集团侧 4 个人工列**不算**，否则每次提交都误拦）
		if hasFormCheck(form, "ledger_l06_written") {
			if verr := d.verifySUBPostLedgerL06(ctx, bizNo); verr != nil {
				return fail(c, http.StatusInternalServerError, codeInternal, verr.Error())
			}
		}
		// BJ#selection_reason_immutable（when=提交后）：选定理由/采购方式/选定单位
		// 提交即冻结 —— 工具表 R19「不得事后补写」的唯一可执行形态
		if hasFormCheck(form, "selection_reason_immutable") {
			if verr := d.verifyBJPostSubmitImmutability(ctx, &body, bizNo); verr != nil {
				return fail(c, http.StatusInternalServerError, codeInternal, verr.Error())
			}
		}
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role,
		Action: "submit", Resource: "approval", TargetID: bizNo, Result: "allow"})
	inst, err := d.DB.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return ok(c, map[string]any{"biz_no": bizNo, "instance_id": inst.InstanceCode, "status": inst.Status,
		"payment_route": paymentRoute})
}

// boolFromBodyField 从表单字段取布尔值（缺失/非布尔 ⇒ false）。
func boolFromBodyField(fields map[string]any, key string) bool {
	b, _ := fields[key].(bool)
	return b
}

// isChainInputError chain 计算中「输入驱动」的错误 ⇒ 400；其余（查询失败等）⇒ 500。
func isChainInputError(err error) bool {
	return errors.Is(err, chain.ErrUnsupportedDoc) ||
		errors.Is(err, chain.ErrAmountMissing) ||
		errors.Is(err, chain.ErrTierOutOfDoc) ||
		errors.Is(err, chain.ErrRouteMissing) ||
		errors.Is(err, chain.ErrTierNoBand) ||
		errors.Is(err, chain.ErrCategoryInvalid) ||
		errors.Is(err, chain.ErrPaymentInvalid)
}

// mergeProvidedFields 构造提交载荷合并视图（顶层别名 + fields map）。
func mergeProvidedFields(body *approvalSubmitBody, usageL1, usageL2 string) map[string]any {
	provided := make(map[string]any, len(body.Fields)+8)
	for k, v := range body.Fields {
		provided[k] = v
	}
	if body.AmountCents != nil {
		provided["amount_cents"] = *body.AmountCents
	}
	if strings.TrimSpace(body.Supplier) != "" {
		provided["supplier"] = body.Supplier
	}
	if usageL1 != "" {
		provided["usage_category_l1"] = usageL1
	}
	if usageL2 != "" {
		provided["usage_category_l2"] = usageL2
	}
	if strings.TrimSpace(body.PurposeClassL1) != "" {
		provided["purpose_class_l1"] = body.PurposeClassL1
	}
	if strings.TrimSpace(body.PurposeClassL2) != "" {
		provided["purpose_class_l2"] = body.PurposeClassL2
	}
	return provided
}

// orgVerifyAtSubmit 提交时实时回源（FR-M9-17 / M5 / D6 定案）：
//
//	同步 2s 超时；成功 ⇒ ok:true + 在职快照（含部门一致性标记，**不阻断**）；
//	失败/超时 ⇒ ok:false + reason，**告警放行**（绝不因回源失败拦提交）；
//	verifier 未装配 ⇒ not_assembled（可见，不静默）。
//
// 返回标记由 SubmitInput.OrgVerify 合并进 ext_json.org_verify（服务端权威，客户端伪造无效）。
func (d Deps) orgVerifyAtSubmit(ctx context.Context, openID, submitDept string) map[string]any {
	m := map[string]any{"at": time.Now().UTC().Format(time.RFC3339)}
	if d.OrgVerifier == nil {
		m["ok"] = false
		m["reason"] = "verifier_not_assembled"
		d.Log.Warn("提交回源端口未装配（FR-M9-17 标记为 not_assembled）", "open_id", openID)
		return m
	}
	vctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	u, err := d.OrgVerifier.GetUser(vctx, openID)
	if err != nil {
		m["ok"] = false
		m["reason"] = "live_lookup_failed"
		// ★ 告警放行：错误详情进日志（可能含 URL/租户细节），不进 ext_json。
		d.Log.Warn("★ 提交实时回源失败（告警放行，FR-M9-17）", "open_id", openID, "error", err.Error())
		return m
	}
	m["ok"] = true
	m["employee_status"] = u.EmployeeStatus
	m["is_resigned"] = u.IsResigned
	m["is_deleted"] = u.IsDeleted
	// 部门一致性：镜像部门名可取时比对；不一致只记标记（阻断口径归业务，未裁定不自造）。
	if u.PrimaryDepartmentID != "" {
		if dept, dErr := d.DB.GetOrgDepartment(ctx, u.PrimaryDepartmentID); dErr == nil && dept != nil && dept.Name != "" {
			m["live_department"] = dept.Name
			if submitDept != "" && dept.Name != submitDept {
				m["department_mismatch"] = true
			}
		}
	}
	return m
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
	// ★ 解绿框（用户实测反馈 2026-09-28）：任务列表补 `assignee_name` / `assignee_department`
	//   （数据源 t_user_role，**一次批量 IN 查询**防 N+1；查不到 ⇒ 留空，绝不塞 open_id）。
	roleMap := d.assigneeRoleViews(ctx, tasks)
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
		"tasks":         approvalTaskViews(tasks, roleMap),
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

// approvalTaskViews 任务列表视图。
//
// ★ 新增 `assignee_name` / `assignee_department`（解绿框）：取自 roleMap（t_user_role
// 批量查询结果）；**查不到 ⇒ 键留空字符串**（前端回落 `-`），**绝不**把 open_id 塞进
// name —— 契约见 docs/05-API §3.13。
func approvalTaskViews(tasks []store.FlowTask, roleMap map[string]store.UserRole) []map[string]any {
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		name, dept := "", ""
		if r, ok := roleMap[t.AssigneeOpenID]; ok {
			name, dept = r.Name, r.Department
		}
		out = append(out, map[string]any{
			"task_id": t.TaskID, "node_id": t.NodeID, "node_name": t.NodeName,
			"task_order": t.TaskOrder, "round": t.Round, "assignee": t.AssigneeOpenID,
			"assignee_name": name, "assignee_department": dept,
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

// handleAdminApprovalDefsSync 三方审批定义装载（主通道；docs/16 §2-C 通道①）。
//
// ★ 路由：`POST /api/admin/approval/defs/sync`（admin 组；鉴权＝requireSysAdmin，
//
//	与 `/api/admin` 域语义一致，API §3.9）。
//
// ★ 流程：读 `t_config_mapping(map_kind='approval_code')`（map_key＝code、map_value＝doc_type，
//
//	清单正本＝docs/reference/config-mapping.sample.json 的 approval_code 节，11 类）→
//	组装 []approval.DefInput → d.ApprovalDefs.Sync(...)（幂等：approval_code 命中即更新）。
//
// ★ 可见失败（绝不 200 静默，定案 #21 同源纪律）：
//
//	① 清单为空              → 400（未导入配置映射）；
//	② code 仍是占位符       → 400（复用 config.IsPlaceholder，与导入层同一 marker 清单）；
//	③ doc_type 不在 11 类内 → 400；
//	④ 回调 token/域名未配置 → 503（定义无 token 则回调校验恒失败，拒绝装载）。
//
// ★ token 一处配置两侧一致：`Env.ActionCallbackToken`（JX_ACTION_CALLBACK_TOKEN）随
//
//	DefInput.CallbackToken 下发飞书，本地落 `t_approval_def.callback_token` 同值
//	（verifyCallbackToken 按实例 → 定义 → token 常数时间比对）。
//
// ★ 响应返回可核对计数（synced/created/updated/skipped/failed + items 明细），
//
//	供运维自证；部分失败 → HTTP 500 但计数与明细照返（approval.ErrSyncFailed，S7）。
func (d Deps) handleAdminApprovalDefsSync(c echo.Context) error {
	idn, allowed := d.requireSysAdmin(c)
	if !allowed {
		return nil // requireSysAdmin 已写出 403/401 响应并留痕
	}
	ctx := c.Request().Context()
	if d.ApprovalDefs == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "三方审批定义注册表未装配")
	}
	// ④ 回调配置门禁：token / 域名缺一即拒绝装载（不制造"定义已建但回调永远校验失败"的半成品）。
	if d.Env == nil || strings.TrimSpace(d.Env.ActionCallbackToken) == "" {
		return fail(c, http.StatusServiceUnavailable, codeNotReady,
			"回调 token 未配置（JX_ACTION_CALLBACK_TOKEN）：定义无 token 则回调校验恒失败，拒绝装载")
	}
	if d.Env == nil || strings.TrimSpace(d.Env.CallbackDomain) == "" {
		return fail(c, http.StatusServiceUnavailable, codeNotReady,
			"回调域名未配置（JX_CALLBACK_DOMAIN）：无法组装 action_callback_url，拒绝装载")
	}

	// ① 读清单（读库而非启动期 Maps 快照：导入后无需重启即可装载）。
	rows, err := d.DB.ListConfigMappings(ctx, "approval_code")
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	if len(rows) == 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"approval_code 配置清单为空：请先导入配置映射（docs/reference/config-mapping.sample.json "+
				"的 approval_code 节，替换占位符后经配置映射导入写入 t_config_mapping）")
	}

	// ②③ 占位符 / doc_type 二次校验（导入层已拦，此处防绕过导入通道的直写脏数据）。
	var violations []string
	for _, r := range rows {
		code := strings.TrimSpace(r.MapKey)
		dt := strings.TrimSpace(r.MapValue)
		switch {
		case code == "" || dt == "":
			violations = append(violations, fmt.Sprintf("code=%q doc_type=%q：存在空值", code, dt))
		case config.IsPlaceholder(code):
			violations = append(violations,
				fmt.Sprintf("code=%s：仍是未替换的占位符——请填入飞书审批后台的真实 approval_code", code))
		case !inDocTypes(dt):
			violations = append(violations,
				fmt.Sprintf("code=%s：doc_type=%q 不在 11 类（%s）之内", code, dt, strings.Join(config.DocTypes, "/")))
		}
	}
	if len(violations) > 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"approval_code 清单不可装载："+strings.Join(violations, "；"))
	}

	// 组装 DefInput：名称取配置 remark（清单正本各条自带单据名），缺省退回 doc_type；
	// 分组留空（飞书 group_name 可选，COALESCE 保留既有值）；回调 URL/token 来自环境配置。
	//
	// ★★ 开关必须显式传全（2026-09-27 实测教训）：飞书 external_approvals 是 upsert，
	// 未传的开关字段被平台重置为默认 false ⇒ 一次装载就会把飞书侧已配置好的定义
	// （enable_quick_operate=true ⇒「同意/拒绝」两键）重置打没。故此处对齐真机读回基线：
	// EnableQuickOperate / SupportPC / SupportMobile / AllowBatchOperate / SupportBatchRead
	// 显式 true、EnableMarkReaded 显式 false——**不赌平台默认值**（docs/16 V-5）。
	callbackURL := strings.TrimRight(strings.TrimSpace(d.Env.CallbackDomain), "/") + "/approval/external/callback"
	// 发起页指向我方**按单据类型的发起页**（M7/W8：/submit/{doc_type}，历史根路径 "/" 已修正）。
	// ★ 存量修正＝重跑本端点（upsert 对非空 incoming 即覆盖，见 repo_approval_def.go COALESCE 注释）。
	createLinkBase := strings.TrimRight(strings.TrimSpace(d.Env.CallbackDomain), "/")
	inputs := make([]approval.DefInput, 0, len(rows))
	for _, r := range rows {
		dt := strings.TrimSpace(r.MapValue)
		createLink := createLinkBase + "/submit/" + dt
		inputs = append(inputs, approval.DefInput{
			DocType:            dt,
			ApprovalCode:       strings.TrimSpace(r.MapKey),
			Name:               firstNonEmptyStr(strings.TrimSpace(r.Remark), dt),
			CreateLinkPC:       createLink,
			CreateLinkMobile:   createLink,
			SupportPC:          true,
			SupportMobile:      true,
			EnableQuickOperate: true,
			AllowBatchOperate:  true,
			SupportBatchRead:   true,
			EnableMarkReaded:   false,
			CallbackURL:        callbackURL,
			CallbackToken:      d.Env.ActionCallbackToken,
		})
	}

	res, err := d.ApprovalDefs.Sync(ctx, inputs)
	var created, updated int
	for _, it := range res.Items {
		if it.Err != "" {
			continue
		}
		if it.Created {
			created++
		} else {
			updated++
		}
	}
	counts := map[string]any{
		"synced":  created + updated,
		"created": created,
		"updated": updated,
		"skipped": 0, // 占位符/空清单等在上方已整体拒绝，不存在部分跳过
		"failed":  res.Failed,
		"items":   res.Items,
	}
	if err != nil {
		// 部分失败必须可见（approval.ErrSyncFailed）：500 + 计数与明细照返，运维可核对失败条目。
		return c.JSON(http.StatusInternalServerError, Envelope{
			Code: codeInternal, Data: counts, Message: err.Error(), TraceID: traceID(c),
		})
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role,
		Action: "approval_defs_sync", Resource: "approval", Result: "allow",
		DetailJSON: fmt.Sprintf(`{"synced":%d,"created":%d,"updated":%d}`,
			created+updated, created, updated)})
	return ok(c, counts)
}

// inDocTypes 判断 doc_type 是否在 11 类清单内（清单正本＝config.DocTypes，不复制值）。
func inDocTypes(dt string) bool {
	for _, v := range config.DocTypes {
		if v == dt {
			return true
		}
	}
	return false
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
	case errors.Is(err, flow.ErrInvalidSubmit), errors.Is(err, flow.ErrInvalidToken),
		errors.Is(err, flow.ErrInvalidDesignation), errors.Is(err, flow.ErrInvalidNodeField),
		errors.Is(err, store.ErrStagingNotBindable):
		return fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
	default:
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
}
