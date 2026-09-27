package flow

import (
	"context"
	"log/slog"
	"sort"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// notify.go —— 通知（01a §4.7「应有通知集合」；04a §5.5 漏发可检出）。
//
// ★ 两阶段落盘：**先落 `EXPECTED`**（按 §4.7 规则算出的"应有集合"），**再由发送结果回填 `SENT`/`FAILED`**。
// ★ 漏发判定 ＝「`EXPECTED` 中**未变为 `SENT`** 的记录」（`store.ListUnsentNotify`）。
//
//	★ **不能用"实时算"判漏发** —— 实时算永远自洽，检不出"该发没发"（这正是本条口径存在的原因）。

// 通知渠道。
const (
	ChannelBot   = "feishu_bot"
	ChannelInApp = "inapp"
)

// Sender 通知发送端口（Bot / 站内）。可为 nil → 只落 `EXPECTED`（供重试 / 告警）。
//
// ★ 第 3 批扩展（docs/16 §2-F 关联项）：签名加 `bizNo` —— 真实发送实现
// （feishu.NotifySender → POST /approval/v1/message/send）需按单定位实例才能组装合法报文
// （申请人 title_user_id / 审批名 / 摘要 / 「查看详情」链接均依赖 biz_no），
// 无 biz_no 只能发空壳消息 ⇒ 端口必须携带。既有 FakeSender 同步适配。
type Sender interface {
	Send(ctx context.Context, bizNo, targetOpenID, event string) error
}

// notifyOnType 需要「通知已审批通过者」的事件类型（01a §4.7：转交 / 回退 / 撤回）。
func notifyOnType(t FlowEventType) bool {
	switch t {
	case EventTransferred, EventRolledBack, EventCanceled:
		return true
	default:
		return false
	}
}

// ExpectedNotifyTargets 计算「应有通知集合」（01a §4.7），纯函数（顺序稳定）：
//   - 该实例中 `status == APPROVED` 的审批人；
//   - **去重**；**排除操作人本人**；**不含抄送**；已 `TRANSFERRED` 的原审批人不在 APPROVED 集合内（天然排除）。
func ExpectedNotifyTargets(tasks []store.FlowTask, actorOpenID string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tasks {
		if t.Status != TaskApproved {
			continue
		}
		if t.AssigneeOpenID == "" || t.AssigneeOpenID == actorOpenID {
			continue
		}
		if seen[t.AssigneeOpenID] {
			continue
		}
		seen[t.AssigneeOpenID] = true
		out = append(out, t.AssigneeOpenID)
	}
	sort.Strings(out)
	return out
}

// Notifier 通知订阅者：先落 EXPECTED，再发送并回填 SENT/FAILED。
type Notifier struct {
	db     *store.DB
	sender Sender
	log    *slog.Logger
}

// NewNotifier 构造通知订阅者（sender 可为 nil）。
func NewNotifier(db *store.DB, sender Sender, log *slog.Logger) *Notifier {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Notifier{db: db, sender: sender, log: log}
}

// OnFlowEvent 处理流程事件（实现 Subscriber）。
func (n *Notifier) OnFlowEvent(ctx context.Context, ev FlowEvent) {
	if !notifyOnType(ev.Type) {
		return
	}
	// ★ 优先用事件携带的"应有集合"（操作时、状态变更前算好）；缺失时才回退按当前任务算。
	targets := ev.NotifyTargets
	if targets == nil {
		tasks, err := n.db.ListFlowTasks(ctx, ev.BizNo)
		if err != nil {
			n.log.Error("通知：读取任务失败", "biz_no", ev.BizNo, "error", err.Error())
			return
		}
		targets = ExpectedNotifyTargets(tasks, ev.ActorOpenID)
	}
	event := string(ev.Type)

	// ① 先落 EXPECTED（幂等，仅新增，不覆盖已 SENT）。
	for _, tgt := range targets {
		if err := n.db.EnsureNotifyExpected(ctx, &store.NotifyLog{
			BizNo: ev.BizNo, TargetOpenID: tgt, Channel: ChannelBot, Event: event,
			Status: store.NotifyExpected,
		}); err != nil {
			n.log.Error("通知：登记 EXPECTED 失败", "biz_no", ev.BizNo, "target", tgt, "error", err.Error())
		}
	}

	// ② 发送 + 回填（sender=nil 时停在此 —— 记录仍是 EXPECTED，可被漏发检出/重试）。
	if n.sender == nil || len(targets) == 0 {
		return
	}
	for _, tgt := range targets {
		status := store.NotifySent
		var lastErr string
		if err := n.sender.Send(ctx, ev.BizNo, tgt, event); err != nil {
			status = store.NotifyFailed
			lastErr = err.Error()
		}
		if err := n.db.UpdateNotifyStatus(ctx, ev.BizNo, tgt, event, ChannelBot, status, lastErr); err != nil {
			n.log.Error("通知：回填状态失败", "biz_no", ev.BizNo, "target", tgt, "error", err.Error())
		}
	}
}

// 编译期断言：Notifier 实现 Subscriber。
var _ Subscriber = (*Notifier)(nil)
