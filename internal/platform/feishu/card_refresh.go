package feishu

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// card_refresh.go —— 审批 Bot 卡片「推进成功后主动刷新」（本批核心；2026-09-28 实测定稿）。
//
// ★★ 实测背景（reference/README.md 台账，直接采信）：
//
//   - **Bot 卡片与「审批应用内的单据状态」是两处、分开更新**：回调处理成功后
//     （accepted=true、我方状态机推进、审批应用显示"已同意"），**卡片仍带「同意/拒绝」
//     两键、未被刷新** —— 平台承诺的"自动更新卡片"在实测中未发生 ⇒ **不依赖平台自动
//     更新，我方处理成功后主动调 message/update**（body={"message_id","status"}，
//     实测 code=0）。
//   - **卡片操作的回调报文【不带】message_id**（实测留痕为空字段）⇒ 卡片 id 只能由
//     发通知侧（NotifySender.Send → message/send 回执 → t_notify_log.message_id，0014）
//     自己记录，本组件按 (biz_no, 审批人) 反查。
//
// ★ 触发点（与 RepairCardFeedback 的职责区分，勿混，见 message.go 注释）：
//
//	本组件＝**推进成功**路径：flow 状态机推进成功（act → 事务提交后 emit）后，
//	由 bootstrap 的 flowCardRefreshSubscriber 订阅 TASK_APPROVED / TASK_REJECTED 事件
//	调用 Refresh，把该审批人持有的待办卡片刷成终态（APPROVED / REJECTED）。
//
// ★ 纪律（逐条执行）：
//
//   - **「落盘即 200」（#69）**：刷新失败只记日志，绝不影响回调的 200 —— 本组件由
//     flow.emit 分发调用，emit 已隔离订阅者错误/panic；Refresh 自身不返回 error、不 panic；
//   - **查不到 message_id（含无通知行）⇒ 跳过且不报错**，记 info：并非每张卡片都经由
//     我方 message/send 发出（例如手工推的调试卷）；
//   - **幂等**：同一 (biz_no, task_id, status) 进程内只成功刷一次 —— TASK_APPROVED 等
//     事件可能被多条路径触发（回调推进 / 修复循环重驱动 / 页面操作），不防重会对同一张
//     卡重复调 message/update（配额浪费 + 卡片闪动）。进程重启后 seen 集清空 ⇒ 至多重刷
//     一次；message/update 本身幂等（按 message_id 覆盖状态），无数据损坏面。
type CardRefresher struct {
	db     *store.DB
	client MessageUpdateClient
	log    *slog.Logger

	mu   sync.Mutex
	done map[string]bool // 已成功刷新的 key（biz_no/task_id/status）
}

// notifyTodoEventPrefix 「新待办产生」通知的事件键前缀。
// ★ feishu 包不反向依赖领域包 flow：字面量与 flow.eventTaskActivated /
// flow.activationEventKey（`TASK_ACTIVATED:<task_id>`）对齐 —— 我方发给审批人的
// 待办卡片通知行（t_notify_log.event）即以此前缀落库。
const notifyTodoEventPrefix = "TASK_ACTIVATED"

// NewCardRefresher 构造卡片刷新器（client 为 nil 时一切调用为 no-op）。
func NewCardRefresher(db *store.DB, client MessageUpdateClient, log *slog.Logger) *CardRefresher {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &CardRefresher{
		db:     db,
		client: client,
		log:    observ.WithComponent(log, "feishu"),
		done:   map[string]bool{},
	}
}

// Refresh 推进成功后，把该任务审批人持有的待办卡片刷新为终态 status（APPROVED / REJECTED）。
//
// 步骤（任一前置缺失 ⇒ 跳过 + 日志，**绝不报错给调用方**——emit 纪律 / 落盘即 200）：
//  1. 幂等防重：同 (biz_no, task_id, status) 已成功刷过 ⇒ 直接返回；
//  2. 由 task_id 定位任务行，取审批人 assignee_open_id（并校验任务归属该 biz_no，防串单）；
//  3. 查 t_notify_log 该 (biz_no, assignee) 最近的待办通知行（event 前缀 TASK_ACTIVATED）
//     的 message_id；为空/查不到 ⇒ 跳过且不报错（info）；
//  4. 调 message/update(message_id, status)；失败只记 Error（不标记 done ⇒ 下次触发重试）。
func (r *CardRefresher) Refresh(ctx context.Context, bizNo, taskID, status string) {
	if r == nil || r.client == nil || r.db == nil {
		return
	}
	bizNo = strings.TrimSpace(bizNo)
	taskID = strings.TrimSpace(taskID)
	status = strings.TrimSpace(status)
	if bizNo == "" || taskID == "" || status == "" {
		r.log.Warn("卡片刷新入参缺失，跳过", "biz_no", bizNo, "task_id", taskID, "status", status)
		return
	}

	// ① 进程内防重：同 (单号, 任务, 终态) 只成功刷一次。
	key := fmt.Sprintf("%s/%s/%s", bizNo, taskID, status)
	r.mu.Lock()
	if r.done[key] {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	// ② 定位任务 → 审批人（★ 校验任务归属，防 task_id 与 biz_no 串单）。
	task, err := r.db.GetFlowTask(ctx, taskID)
	if err != nil {
		r.log.Warn("卡片刷新：定位任务失败，跳过（不影响主流程）",
			"biz_no", bizNo, "task_id", taskID, "error", err.Error())
		return
	}
	if task.BizNo != bizNo {
		r.log.Warn("卡片刷新：任务不属于该实例（疑似串单），跳过", "biz_no", bizNo, "task_id", taskID, "task_biz_no", task.BizNo)
		return
	}
	assignee := strings.TrimSpace(task.AssigneeOpenID)
	if assignee == "" {
		r.log.Warn("卡片刷新：任务无审批人，跳过", "biz_no", bizNo, "task_id", taskID)
		return
	}

	// ③ 反查该审批人最近的待办卡片 message_id（0014 列；NotifySender 发送时落盘）。
	//    无行 / 列为空 ⇒ 正常态（卡片非我方 message/send 发出）⇒ 跳过、不报错。
	messageID, err := r.db.LatestNotifyMessageID(ctx, bizNo, assignee, notifyChannelBot, notifyTodoEventPrefix)
	if err != nil {
		r.log.Warn("卡片刷新：查询通知 message_id 失败，跳过（不影响主流程）",
			"biz_no", bizNo, "task_id", taskID, "assignee", assignee, "error", err.Error())
		return
	}
	if strings.TrimSpace(messageID) == "" {
		r.log.Info("卡片刷新：无我方发出的待办卡片记录（message_id 为空/无通知行），跳过",
			"biz_no", bizNo, "task_id", taskID, "assignee", assignee)
		return
	}

	// ④ 主动刷新卡片（实测 body={"message_id","status"}；status 与审批终态一致）。
	//    ★ 失败只记日志：绝不影响回调的 200（#69 落盘即 200）；不标记 done ⇒ 下次触发重试。
	if err := r.client.UpdateApprovalMessage(ctx, messageID, status); err != nil {
		r.log.Error("卡片刷新失败（只记日志，不回滚业务；下次触发重试）",
			"biz_no", bizNo, "task_id", taskID, "assignee", assignee,
			"message_id", messageID, "status", status, "error", err.Error())
		return
	}
	r.mu.Lock()
	r.done[key] = true
	r.mu.Unlock()
	r.log.Info("待办卡片已主动刷新为终态（实测平台不自动刷新卡片）",
		"biz_no", bizNo, "task_id", taskID, "assignee", assignee,
		"message_id", messageID, "status", status)
}
