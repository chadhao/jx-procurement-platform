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
//
// 本文件承载**两条互不混用的通知口径**（FR-M0-17 追加接线）：
//
//  1. **通知已审批通过者**（01a §4.7：转交 / 回退 / 撤回）—— 收件人＝`ExpectedNotifyTargets`
//     （该实例 `status==APPROVED` 的审批人）；既有行为，保持原样。
//  2. **通知新待办人**（FR-M0-17；02-UseCase UC-23 通知时机）—— 收件人＝**被新激活（RELEASED）
//     任务的 assignee**（`ActivatedNotifyTargets`）。与口径 1 的收件人集合**完全不同**，不得复用。
//
// ★ 口径 2 的触发点选择（设计定案，docs/16 FR-M0-17）：
// 不新增流程事件类型、不侵入状态机 —— 「任务激活」（HELD → RELEASED）分散在 Submit 建链 /
// releaseNextTx 逐级释放 / Transfer 接力 / AddSign 前置 / Rollback 重置 **5 处**，逐点发布新事件
// 侵入面大且语义易与既有 10 类事件混淆。改为**复用既有流转事件**（SUBMITTED / TASK_APPROVED /
// TRANSFERRED / ADDED_SIGN / ROLLED_BACK —— 恰为所有可能改变「可办理任务」的操作所发事件），
// 在 `notifyActivated` 中按**事务已提交后的任务行**推导新激活任务：顺序会签不变量（04a §2.3）
// 保证任一时刻整实例「可办理」（RELEASED ∧ PENDING）任务**至多 1 个** ⇒ 事件后查到的可办理任务
// 即本次新激活者。HELD（未轮到）任务天然不在结果中 —— 未激活不发通知（UC-23）。

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

// activateOnType 可能改变「新激活任务」的流转事件类型（口径 2 触发面）：
//   - SUBMITTED：提交建链释放**首位**任务（createTasksTx，04a §2.3）；
//   - TASK_APPROVED：同意后 advanceTx → releaseNextTx 逐级释放**下一位**；
//   - TRANSFERRED：转交追加**立即 RELEASED** 的接力任务（ops.go Transfer）；
//   - ADDED_SIGN：前置加签新增者**先审**（立即 RELEASED；后置加签由 releaseNextTx 覆盖，
//     其「可办理任务不变」的空转情形由幂等去重吸收，见 notifyActivated）；
//   - ROLLED_BACK：被回退节点首位任务重置为 RELEASED（ops.go Rollback）。
//
// ★ 不含 TASK_REJECTED 与实例终态事件：它们只伴随终态化发生（在途任务被置 DONE），
// 此后不存在可办理任务，按状态推导必为空 —— 无需触发。
func activateOnType(t FlowEventType) bool {
	switch t {
	case EventSubmitted, EventTaskApproved, EventTransferred, EventAddedSign, EventRolledBack:
		return true
	default:
		return false
	}
}

// eventTaskActivated 「新待办产生」通知的事件键前缀（口径 2；落 t_notify_log.event，
// 并作为 event 参数传给 Sender → 飞书 message/send 文案映射）。
// ★ 实际键＝`TASK_ACTIVATED:<task_id>`：携带任务标识，使幂等去重**精确到任务**——
// 同一 (biz_no, task_id) 的重复触发不重发；转交产生的**新 task_id** 是新键、正常提醒。
const eventTaskActivated = "TASK_ACTIVATED"

// activationEventKey 由任务 ID 构造「新待办产生」通知的事件键。
func activationEventKey(taskID string) string { return eventTaskActivated + ":" + taskID }

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

// ActivatedNotifyTargets 计算「新待办产生」的收件人（FR-M0-17；02-UseCase UC-23 通知时机），
// 纯函数（按 TaskID 排序、顺序稳定）：
//   - 收件人＝当前「可办理」（`RELEASED ∧ PENDING`）任务的 assignee（返回**任务行**，
//     供调用方取 `TaskID` 构造幂等事件键）。顺序会签不变量（04a §2.3）保证至多 1 个；
//     若因异常出现多个，逐个通知（fail-open 于通知、不影响状态机）。
//   - **排除空 assignee / 操作人本人**；按 TaskID 去重。
//   - ★ HELD（未激活）任务不出现在结果中 —— 未轮到不发通知（UC-23：未激活 task 不发通知，
//     否则加签人的提醒会提前到达）。
//
// ★ 与 `ExpectedNotifyTargets`（已通过者口径）收件人集合**完全不同**，两者不得混用。
func ActivatedNotifyTargets(tasks []store.FlowTask, actorOpenID string) []store.FlowTask {
	seen := map[string]bool{}
	var out []store.FlowTask
	for _, t := range tasks {
		if t.Status != TaskPending || t.ReleaseState != ReleaseReleased {
			continue // 仅「可办理」任务 ＝ 本次新激活者
		}
		if t.AssigneeOpenID == "" || t.AssigneeOpenID == actorOpenID {
			continue
		}
		if seen[t.TaskID] {
			continue
		}
		seen[t.TaskID] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
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

// OnFlowEvent 处理流程事件（实现 Subscriber）。按事件类型分派到两条通知口径：
//   - 既有口径（转交/回退/撤回 → 已通过者）：行为保持原样，不改；
//   - 新增口径（FR-M0-17 新待办产生 → 新激活任务 assignee）：见 notifyActivated。
//
// ★ 两口径对同一事件可能同时生效（如 TRANSFERRED：既通知已通过者、又通知接力人），
// 事件键不同（`TRANSFERRED` vs `TASK_ACTIVATED:<task_id>`）→ t_notify_log 互不干扰。
func (n *Notifier) OnFlowEvent(ctx context.Context, ev FlowEvent) {
	if notifyOnType(ev.Type) {
		n.notifyApproved(ctx, ev)
	}
	if activateOnType(ev.Type) {
		n.notifyActivated(ctx, ev)
	}
}

// notifyApproved 既有口径：通知「已审批通过者」（01a §4.7）。
// ★ 行为与 FR-M0-17 接线前**逐字节等价**（原 OnFlowEvent 主体原样搬移，不改任何语义）。
func (n *Notifier) notifyApproved(ctx context.Context, ev FlowEvent) {
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

// notifyActivated 新增口径（FR-M0-17）：「新待办产生」→ 通知被新激活任务的 assignee。
//
// ★ 事务已提交后按任务行推导（见文件头设计定案）：查询失败/无激活任务直接返回，
// **绝不影响业务状态推进**（与「落盘即 200」纪律一致）。
//
// ★ 两阶段机制沿用：EnsureNotifyExpected（EXPECTED）→ Send → UpdateNotifyStatus（SENT/FAILED），
// channel＝既有 `feishu_bot`。
//
// ★ 幂等（不重复轰炸）：发送前先查 t_notify_log 既有行（store.HasNotifyLog），命中即跳过。
// 去重键＝(biz_no, target_open_id, `TASK_ACTIVATED:<task_id>`, channel)，即**精确到任务**：
//   - 同一激活被多个事件重复触发（如后置加签的 ADDED_SIGN 空转、repair 重驱动的幂等 no-op
//     根本不发事件）→ 同键命中，不重发；
//   - 回退重激活（round+1 但 task_id 不变）→ 同键命中，不重复轰炸（该场景下被重开者本就在
//     既有口径的「已通过者」集合内，会收到 ROLLED_BACK 知会，不漏知会）；
//   - 转交产生**新 task_id** → 新键 → 接力人正常提醒（不漏）。
func (n *Notifier) notifyActivated(ctx context.Context, ev FlowEvent) {
	tasks, err := n.db.ListFlowTasks(ctx, ev.BizNo)
	if err != nil {
		n.log.Error("通知：读取任务失败（新待办口径）", "biz_no", ev.BizNo, "error", err.Error())
		return
	}
	for _, t := range ActivatedNotifyTargets(tasks, ev.ActorOpenID) {
		event := activationEventKey(t.TaskID)
		// 幂等：既有行（任意状态）⇒ 该 (单号, 任务) 的待办提醒已登记过，绝不重发。
		exists, err := n.db.HasNotifyLog(ctx, ev.BizNo, t.AssigneeOpenID, event, ChannelBot)
		if err != nil {
			n.log.Error("通知：查询既有行失败（新待办口径）",
				"biz_no", ev.BizNo, "target", t.AssigneeOpenID, "error", err.Error())
			continue
		}
		if exists {
			continue
		}
		// ① 先落 EXPECTED（漏发可检出；sender=nil 时留待重试/告警）。
		if err := n.db.EnsureNotifyExpected(ctx, &store.NotifyLog{
			BizNo: ev.BizNo, TargetOpenID: t.AssigneeOpenID, Channel: ChannelBot, Event: event,
			Status: store.NotifyExpected,
		}); err != nil {
			n.log.Error("通知：登记 EXPECTED 失败（新待办口径）",
				"biz_no", ev.BizNo, "target", t.AssigneeOpenID, "error", err.Error())
		}
		// ② 发送 + 回填。
		if n.sender == nil {
			continue
		}
		status := store.NotifySent
		var lastErr string
		if err := n.sender.Send(ctx, ev.BizNo, t.AssigneeOpenID, event); err != nil {
			status = store.NotifyFailed
			lastErr = err.Error()
		}
		if err := n.db.UpdateNotifyStatus(ctx, ev.BizNo, t.AssigneeOpenID, event, ChannelBot, status, lastErr); err != nil {
			n.log.Error("通知：回填状态失败（新待办口径）",
				"biz_no", ev.BizNo, "target", t.AssigneeOpenID, "error", err.Error())
		}
	}
}

// 编译期断言：Notifier 实现 Subscriber。
var _ Subscriber = (*Notifier)(nil)
