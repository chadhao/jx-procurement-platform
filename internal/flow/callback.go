package flow

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// callback.go —— 入站回调核心逻辑（04a §4 / §4.3；S8/S11）。
//
// ★ 本文件只提供**核心逻辑**（导出给未来的 handler 调用），**不注册任何路由 / 不感知 HTTP**。
//
// ★ 同步路径纪律（04a §4.4「落盘即 200」）：**只做「校验 + 写 op_log + 入队」**，
// 真正的状态机推进在**异步**（worker 消费 op_log 后调 `Advancer`）→ 同步路径**毫秒级可返回**。
//   若把推进放进同步路径，超时（口径不一，QV2-02）下会**重复推进**与**阻塞飞书回调**两害并发。
//
// ★ 幂等（04a §4.3）：`t_flow_op_log` 对 APPROVE/REJECT 有 (biz_no,task_id,op_type) 唯一约束 →
//   重复回调被 `INSERT OR IGNORE` 吸收，返回 Duplicate=true，**绝不二次推进**。
//
// ★★ 幂等键纪律（README 定案 **#62**）：**准入失败的请求不得消耗幂等键**——
//   幂等键的占用必须发生在**准入通过之后**。否则一次被拒（非本人）的回调即占键 →
//   其后**同键的真实回调**被判 `Duplicate=true` → `200 no-op` **静默不推进**（飞书侧以为成功）。
//   本纪律由「①token → ②准入(`admitCallback`) → ③落盘(`recordCallback`)」的**顺序**保证（见 `HandleCallback`）。

// ErrInvalidToken 回调 token 校验失败（必须**可见地拒绝**，绝不静默放行）。
var ErrInvalidToken = errors.New("flow: 回调 token 校验失败")

// CallbackRequest 入站回调请求（飞书「同意 / 拒绝」两键；`action_context` 已由 handler 解析到字段）。
type CallbackRequest struct {
	Token  string
	BizNo  string
	TaskID string
	// InstanceCode 报文携带的 instance_id（我方口径＝`{app_id}:{biz_no}`，04a §3.3）。
	// 用于「防串单」一致性校验（(c)：报文 instance_id ↔ biz_no 解析结果）。
	// ★ 可空：缺省（旧报文未带）则**不校验**该维度，不改变既有可达路径（向后兼容）。
	InstanceCode   string
	OpType         string // APPROVE / REJECT
	OperatorOpenID string
	Reason         string
}

// CallbackResult 同步返回结果。
type CallbackResult struct {
	Accepted  bool // 已受理（写入 op_log 或幂等命中）
	Duplicate bool // 幂等命中（重复回调）
}

// Advancer 异步推进端口：worker 消费 op_log 后调用它做真正的状态机推进。
//
//	生产装配＝「入队后立即返回」；测试可注入同步实现以端到端验证。
type Advancer func(ctx context.Context, req CallbackRequest) error

// SetCallbackAdvancer 注入异步推进端口（nil＝仅落 op_log，不入队）。
func (s *Service) SetCallbackAdvancer(fn Advancer) { s.advancer = fn }

// HandleCallback 处理一次回调：token 校验 → **准入** → 幂等落盘 → 入队（毫秒级返回）。
//
// ★ 顺序即纪律（定案 #62）：**准入（authorization）必须先于 `recordCallback`（占幂等键）**。
//   - ① `verifyCallbackToken`：报文**真实性**（共享密钥，可见地拒绝）；
//   - ② `admitCallback`：**授权**——`operator==assignee` ∧ 任务归属 ∧ `instance_code` 一致；
//   - ③ `recordCallback`：**只有已通过准入者**才落 op_log / 占键；
//   - ④ `advancer`：异步推进（`act` 内**保留**同款鉴权作纵深防御）。
func (s *Service) HandleCallback(ctx context.Context, req CallbackRequest) (CallbackResult, error) {
	op := strings.ToUpper(strings.TrimSpace(req.OpType))
	if op != OpApprove && op != OpReject {
		return CallbackResult{}, fmt.Errorf("%w: 非法回调操作 %q", ErrIllegalTransition, req.OpType)
	}
	if strings.TrimSpace(req.BizNo) == "" || strings.TrimSpace(req.TaskID) == "" {
		return CallbackResult{}, fmt.Errorf("%w: 回调缺少 biz_no/task_id", ErrInvalidSubmit)
	}

	// (a) 入口**显式拒空 operator**：无身份即无法鉴权 → 必须**可见地拒绝**，
	//     绝不「空身份＝匿名放行」，更不得让空身份**占键**（那会污染幂等键，堵死同键真实回调）。
	if strings.TrimSpace(req.OperatorOpenID) == "" {
		return CallbackResult{}, fmt.Errorf("%w: 回调缺少 operator（无法鉴权）", ErrInvalidSubmit)
	}

	// ① token 校验（报文真实性；必须可见地拒绝）。
	if err := s.verifyCallbackToken(ctx, req.BizNo, req.Token); err != nil {
		return CallbackResult{}, err
	}

	// ② ★★ 准入（authorization）—— 身份鉴权**前移到入口**，且**严格先于** `recordCallback`。
	//    定案 #62：准入失败的请求**不得占用幂等键**。若把准入放到 `act`（在 `recordCallback`
	//    之后、由 advancer 调用），一次被拒（非本人）的回调就会写入 (biz_no,task_id,op_type)
	//    去重键 → 其后**同键的真实回调**被 `INSERT OR IGNORE` 判为重复 → `200 no-op` **静默不推进**，
	//    而飞书侧以为成功（本仓库头号红线＝静默）。
	//    ⇒ 只有**通过准入**的回调，才允许进行第 ③ 步落盘 / 占键。
	if err := s.admitCallback(ctx, req, op); err != nil {
		return CallbackResult{}, err
	}

	// ③ 幂等落盘（同步路径唯一的写；执行到此处＝该请求**已通过准入**）。
	inserted, err := s.recordCallback(ctx, req, op)
	if err != nil {
		return CallbackResult{}, err
	}
	if !inserted {
		// 重复回调：幂等 no-op，返回 Accepted（对飞书仍应 200）。
		return CallbackResult{Accepted: true, Duplicate: true}, nil
	}

	// ④ 入队（异步推进）——同步路径到此返回。
	if s.advancer != nil {
		if err := s.advancer(ctx, req); err != nil {
			// 已落盘：推进失败由 worker 重试；此处把错误透出以便观测，但回调本身已受理。
			return CallbackResult{Accepted: true}, err
		}
	}
	return CallbackResult{Accepted: true}, nil
}

// verifyCallbackToken 校验回调 token：按 biz_no → 实例 → approval_code → 定义 → callback_token。
//
// ★ 定义未配置 token → **拒绝**（不静默放行）；★ 常数时间比较（防时序侧信道）。
//
// ★ (b) `biz_no` 不存在 → 返回**可见的 4xx 语义**（`ErrInvalidSubmit`，handler 已映射 → 400），
// 而**非 500**：未知 `biz_no` 属**请求问题**、不是服务端故障；返回 500 会让飞书**无限重试**
// 且掩盖真实原因（本仓库头号红线＝静默 / 误报）。
func (s *Service) verifyCallbackToken(ctx context.Context, bizNo, token string) error {
	inst, err := s.db.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: 回调 biz_no=%s 无对应实例", ErrInvalidSubmit, bizNo)
		}
		return fmt.Errorf("flow: 回调定位实例失败: %w", err)
	}
	def, err := s.db.GetApprovalDef(ctx, inst.ApprovalCode)
	if err != nil {
		return fmt.Errorf("flow: 回调定位审批定义失败: %w", err)
	}
	want := strings.TrimSpace(def.CallbackToken)
	got := strings.TrimSpace(token)
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		return ErrInvalidToken
	}
	return nil
}

// admitCallback 回调**准入**（authorization）：在**占幂等键之前**判定该回调是否有权推进。
//
// 三项校验（任一不过即**可见地拒绝**，且**不**落 op_log）：
//   - (c) 报文 `instance_code` 与 `biz_no` 解析结果**一致**（防**串单**：把 A 单的 instance_id 配到 B 单）；
//   - 任务存在且**归属**该 `biz_no`（防用 A 单的 `task_id` 推 B 单）；
//   - `operator == assignee`（**同意/拒绝须本人**；用户第二轮口径 ⑥ 逐字：
//     「加签是会签；代理人可以转交或者退回，但是需要通知这个审批单内已经审批通过的所有人。」
//     → 代理人只能「转交 / 退回」，**不得代签**）。
//
// ★ 为何在入口而非仅在 `act` 内：`act` 由 advancer 在 `recordCallback` **之后**调用 ——
//
//	若只在 `act` 校验，被拒回调**已占键**（定案 #62）。入口准入＝把「是否放行」与「是否占键」**解耦**。
//	`act` 内**保留**同款鉴权作**纵深防御**（防 advancer 被绕过直调）。
func (s *Service) admitCallback(ctx context.Context, req CallbackRequest, op string) error {
	// (c) 防串单：仅当报文携带 `instance_code` 时校验（空＝沿用旧报文，不校验该维度）。
	if want := strings.TrimSpace(req.InstanceCode); want != "" {
		if got := s.instanceCode(req.BizNo); want != got {
			return fmt.Errorf("%w: 回调 instance_code=%q 与 biz_no=%q 解析结果 %q 不一致（防串单）",
				ErrInvalidSubmit, want, req.BizNo, got)
		}
	}

	task, err := s.db.GetFlowTask(ctx, req.TaskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 任务不存在 → 同 (b)：可见的 4xx（非 500）。
			return fmt.Errorf("%w: 回调 task_id=%s 无对应任务", ErrInvalidSubmit, req.TaskID)
		}
		return fmt.Errorf("flow: 回调定位任务失败: %w", err)
	}
	if task.BizNo != req.BizNo {
		return fmt.Errorf("%w: 任务 %s 不属于实例 %s（回调与任务不匹配）",
			ErrIllegalTransition, req.TaskID, req.BizNo)
	}

	operator := strings.TrimSpace(req.OperatorOpenID)
	if task.AssigneeOpenID != operator {
		return fmt.Errorf("%w: %s 非任务 %s 的审批人，不可执行 %s（同意/拒绝须本人；准入失败不占幂等键）",
			ErrNotAssignee, operator, req.TaskID, op)
	}
	return nil
}

// recordCallback 幂等写 t_flow_op_log，返回本次是否真的写入（false＝幂等命中）。
//
// ★ 调用前提：请求**已通过 `admitCallback` 准入**（见 `HandleCallback` 顺序纪律，定案 #62）。
func (s *Service) recordCallback(ctx context.Context, req CallbackRequest, op string) (bool, error) {
	var inserted bool
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		inserted, err = s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: req.BizNo, TaskID: req.TaskID, OpType: op,
			ActorOpenID: req.OperatorOpenID, Reason: req.Reason,
			FromStatus: TaskPending, ToStatus: op, CreatedAt: time.Now(),
		})
		return err
	})
	return inserted, err
}
