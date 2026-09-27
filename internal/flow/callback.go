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

// ErrInvalidToken 回调 token 校验失败（必须**可见地拒绝**，绝不静默放行）。
var ErrInvalidToken = errors.New("flow: 回调 token 校验失败")

// CallbackRequest 入站回调请求（飞书「同意 / 拒绝」两键；`action_context` 已由 handler 解析到字段）。
type CallbackRequest struct {
	Token          string
	BizNo          string
	TaskID         string
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

// HandleCallback 处理一次回调：token 校验 → 幂等落盘 → 入队（毫秒级返回）。
func (s *Service) HandleCallback(ctx context.Context, req CallbackRequest) (CallbackResult, error) {
	op := strings.ToUpper(strings.TrimSpace(req.OpType))
	if op != OpApprove && op != OpReject {
		return CallbackResult{}, fmt.Errorf("%w: 非法回调操作 %q", ErrIllegalTransition, req.OpType)
	}
	if strings.TrimSpace(req.BizNo) == "" || strings.TrimSpace(req.TaskID) == "" {
		return CallbackResult{}, fmt.Errorf("%w: 回调缺少 biz_no/task_id", ErrInvalidSubmit)
	}

	// ① token 校验（必须可见地拒绝）。
	if err := s.verifyCallbackToken(ctx, req.BizNo, req.Token); err != nil {
		return CallbackResult{}, err
	}

	// ② 幂等落盘（同步路径唯一的写）。
	inserted, err := s.recordCallback(ctx, req, op)
	if err != nil {
		return CallbackResult{}, err
	}
	if !inserted {
		// 重复回调：幂等 no-op，返回 Accepted（对飞书仍应 200）。
		return CallbackResult{Accepted: true, Duplicate: true}, nil
	}

	// ③ 入队（异步推进）——同步路径到此返回。
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
func (s *Service) verifyCallbackToken(ctx context.Context, bizNo, token string) error {
	inst, err := s.db.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
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

// recordCallback 幂等写 t_flow_op_log，返回本次是否真的写入（false＝幂等命中）。
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
