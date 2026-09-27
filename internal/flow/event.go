package flow

import (
	"context"
	"time"
)

// event.go —— 流程事件（04a §2.5：保留事件机制＝通知 + 日志）。
//
// ★ 统一分发（**单一分发点**）：状态迁移在**事务提交之后**由 `emit` 统一分发，
// 订阅者＝通知 / 审计 / 推送 / 台账。★ 为什么必须"事务提交后"：
// 若在事务内分发，订阅者会读到**尚未提交**（或最终回滚）的状态 → 发出错误的通知/推送
// （"审批通过了"却其实回滚了），且**不报错**——静默族。
//
// ★ 订阅者失败**不得影响主流程**：分发只记日志、不向上抛错（审批已落库，通知失败可重试）。

// FlowEventType 流程事件类型。
type FlowEventType string

const (
	// EventSubmitted 实例已提交（生成单号 + 建链）。
	EventSubmitted FlowEventType = "SUBMITTED"
	// EventTaskApproved 某任务被同意。
	EventTaskApproved FlowEventType = "TASK_APPROVED"
	// EventTaskRejected 某任务被拒绝。
	EventTaskRejected FlowEventType = "TASK_REJECTED"
	// EventNodePassed 某节点全员通过。
	EventNodePassed FlowEventType = "NODE_PASSED"
	// EventInstanceApproved 实例终态 APPROVED。
	EventInstanceApproved FlowEventType = "INSTANCE_APPROVED"
	// EventInstanceRejected 实例终态 REJECTED。
	EventInstanceRejected FlowEventType = "INSTANCE_REJECTED"
	// EventTransferred 任务被转交（原审批人退出）。
	EventTransferred FlowEventType = "TRANSFERRED"
	// EventAddedSign 加签（顺序会签 · 队尾）。
	EventAddedSign FlowEventType = "ADDED_SIGN"
	// EventRolledBack 回退（回到之前节点）。
	EventRolledBack FlowEventType = "ROLLED_BACK"
	// EventCanceled 撤回（终态 CANCELED）。
	EventCanceled FlowEventType = "CANCELED"
)

// FlowEvent 一次流程事件（自带定位字段，订阅者无需再查主表即可定位）。
type FlowEvent struct {
	Type         FlowEventType
	BizNo        string
	InstanceCode string
	DocType      string
	NodeID       string
	NodeName     string
	TaskID       string
	ActorOpenID  string
	Reason       string
	At           time.Time
	// NotifyTargets 本次事件应通知的对象（open_id）。★ 仅对"通知已通过者"类事件（转交/回退/撤回）
	// 由**操作时（状态变更前）**算好并带上——因为这些操作会改动任务状态，事后读任务已算不出"曾通过者"。
	// nil＝由订阅者自行按当前状态计算（如普通审批事件）。
	NotifyTargets []string
}

// Subscriber 流程事件订阅者（通知 / 审计 / 推送 / 台账 各自实现）。
type Subscriber interface {
	OnFlowEvent(ctx context.Context, ev FlowEvent)
}

// Subscribe 注册订阅者（可在装配点多次调用）。
func (s *Service) Subscribe(sub Subscriber) {
	if sub == nil {
		return
	}
	s.subs = append(s.subs, sub)
}

// emit 事务提交后统一分发（单点）。
//
// ★ 订阅者内部自行处理错误/重试；此处仅兜底 recover，确保一个订阅者 panic 不拖垮主流程。
func (s *Service) emit(ctx context.Context, evs ...FlowEvent) {
	if len(s.subs) == 0 || len(evs) == 0 {
		return
	}
	for _, ev := range evs {
		for _, sub := range s.subs {
			s.safeNotify(ctx, sub, ev)
		}
	}
}

// safeNotify 调一个订阅者并兜底 recover（订阅者 panic 不得影响主流程与其余订阅者）。
func (s *Service) safeNotify(ctx context.Context, sub Subscriber, ev FlowEvent) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("流程事件订阅者 panic（已隔离）", "event", string(ev.Type), "biz_no", ev.BizNo, "panic", r)
		}
	}()
	sub.OnFlowEvent(ctx, ev)
}
