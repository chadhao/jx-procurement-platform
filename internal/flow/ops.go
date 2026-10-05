package flow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ops.go —— 四操作（01a §4：转交 / 加签 / 回退 / 撤回）。
//
// ★ 明确**不做**（用户口径，不实现、不留半成品挂点）：拿回 · 终止 · 暂存待审 · 催办 · 超时自动审批。
// ★ 只有"转办"，没有"委派"（用户原话）：代理人在本系统内表现为"可转交 / 可退回"，无独立委派模型。
// ★ 撤回（cancel）已由 service.go 的 `Cancel` 实现；本文件实现其余三操作。

// 四操作 op_type（04a §1.1 操作留痕）。
const (
	OpTransfer = "TRANSFER"
	OpAddSign  = "ADDSIGN"
	OpRollback = "ROLLBACK"
)

// AddSignTiming 加签时机（`G2`，01a §4.3）：**操作人在加签时当场选择**。
//
// ★ 缺省/空值 = `AddSignAfter`（后置）—— 保持既有调用方语义不变。
type AddSignTiming string

const (
	// AddSignAfter 后置（默认）：新任务 `task_order = 本节点 max + 1` 追加到**队尾**；
	// 原审批人**须继续等到其后所有人同意**（聚合按 node 全员）。与既有实现一致。
	AddSignAfter AddSignTiming = "AFTER"
	// AddSignBefore 前置：新任务 `task_order = 当前办理人 order`，同节点 `order >= 该值` 者**整体 +1**；
	// **新增者先审**，当前办理人及其后让位。跳过的是**尚未审批**的当前办理人，故不重审已通过者。
	AddSignBefore AddSignTiming = "BEFORE"
)

// ErrNotAssignee 操作者非该任务审批人（越权操作必须**可见地拒绝**）。
var ErrNotAssignee = errors.New("flow: 操作者非该任务审批人")

// ErrOpLimitExceeded 四操作次数上限（N-060 F4 · FR-M9-04/05/06）：
// 转交 ≤3 · 加签 ≤3 · 回退 ≤2（按单据累计；成功才计数 ⇒ 上限＝第 N+1 次可见拒绝）。
var ErrOpLimitExceeded = errors.New("flow: 操作次数已达上限")

// 四操作次数上限（01a FR-M9-04/05/06 正文「上限 3 / 3 / 2」）。
const (
	MaxTransferPerInstance = 3
	MaxAddSignPerInstance  = 3
	MaxRollbackPerInstance = 2
)

// Transfer 转交：原任务 → TRANSFERRED，并**追加**同节点新任务（交给 targetOpenID）。
//
// ★ 推送口径（04a §3.1）：转交**含既有 task 的状态变更**（原 task → TRANSFERRED）+ **追加**新 task，
//
//	**不属于"纯新增"** → 推送用 **`UPDATE`**（非 `REPLACE`）。
//
// ★ 通知（01a §4.7）：**通知该审批单内已审批通过的所有人**（状态变更前算好，随事件带出）。
func (s *Service) Transfer(ctx context.Context, bizNo, taskID, actorOpenID, targetOpenID, targetName, reason string) error {
	at := time.Now()
	var events []FlowEvent
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		inst, err := s.db.GetInstanceByBizNoTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		if isTerminal(inst.Status) {
			return fmt.Errorf("%w: 实例 %s 已终态 %s，不可转交", ErrIllegalTransition, bizNo, inst.Status)
		}
		task, err := s.db.GetFlowTaskTx(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if task.BizNo != bizNo {
			return fmt.Errorf("%w: 任务 %s 不属于实例 %s", ErrIllegalTransition, taskID, bizNo)
		}
		if task.Status != TaskPending {
			return fmt.Errorf("%w: 任务 %s 状态 %s，不可转交", ErrIllegalTransition, taskID, task.Status)
		}
		if task.ReleaseState == ReleaseHeld {
			return fmt.Errorf("%w: 任务 %s（节点 %s）", ErrTaskHeld, taskID, task.NodeID)
		}
		// ★ N-060 F4（FR-M9-04）：转交次数上限 3（按单据累计；第 4 次可见拒绝）。
		if n, cerr := s.db.CountFlowOpsByTypeTx(ctx, tx, bizNo, OpTransfer); cerr != nil {
			return cerr
		} else if n >= MaxTransferPerInstance {
			return fmt.Errorf("%w: 转交已达上限 %d 次（FR-M9-04）", ErrOpLimitExceeded, MaxTransferPerInstance)
		}
		// 本人 **或** 有效代理人（N-060 F4 · FR-M9-04「本人或其代理人」；authorizer 由
		// bootstrap 注入＝bundle 节点角色 → t_role_agent → NodeAllowsAgent 三重判定；
		// nil ⇒ 仅本人（现状不放宽）。
		if task.AssigneeOpenID != actorOpenID &&
			(s.agentAuthorizer == nil || !s.agentAuthorizer(ctx, task, actorOpenID)) {
			return fmt.Errorf("%w: %s 非任务 %s 的审批人", ErrNotAssignee, actorOpenID, taskID)
		}
		if strings.TrimSpace(targetOpenID) == "" {
			return fmt.Errorf("%w: 转交目标为空", ErrInvalidSubmit)
		}
		if targetOpenID == actorOpenID {
			return fmt.Errorf("%w: 转交目标与本人相同", ErrInvalidSubmit)
		}

		preTasks, err := s.db.ListFlowTasksTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		notifyTargets := ExpectedNotifyTargets(preTasks, actorOpenID)

		// 原任务 → TRANSFERRED（原审批人退出）。
		if err := s.db.UpdateFlowTaskStatusTx(ctx, tx, taskID, TaskTransferred, &at); err != nil {
			return err
		}
		// 追加新任务（同 node_id，立即 RELEASED：转交是"当前这一手"的接力）。
		newID, order, err := s.allocNodeTaskIDTx(ctx, tx, bizNo, task.NodeID, targetOpenID, task.Round)
		if err != nil {
			return err
		}
		if err := s.db.UpsertFlowTaskTx(ctx, tx, &store.FlowTask{
			TaskID: newID, BizNo: bizNo, NodeID: task.NodeID, NodeName: task.NodeName,
			NodeSeq: task.NodeSeq, Round: task.Round, AssigneeOpenID: targetOpenID, AssigneeName: targetName,
			Status: TaskPending, ReleaseState: ReleaseReleased, TaskOrder: order,
			CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			return err
		}
		if _, err := s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, NodeID: task.NodeID, TaskID: taskID, OpType: OpTransfer,
			ActorOpenID: actorOpenID, FromStatus: TaskPending, ToStatus: TaskTransferred,
			Reason: reason, CreatedAt: at,
		}); err != nil {
			return err
		}
		if err := s.saveInstanceTx(ctx, tx, inst, at); err != nil {
			return err
		}
		events = append(events, FlowEvent{
			Type: EventTransferred, BizNo: bizNo, InstanceCode: inst.InstanceCode, DocType: inst.DocType,
			NodeID: task.NodeID, NodeName: task.NodeName, TaskID: taskID, ActorOpenID: actorOpenID,
			Reason: reason, At: at, NotifyTargets: notifyTargets,
		})
		return nil
	})
	if err != nil {
		return err
	}
	s.emit(ctx, events...)
	return nil
}

// AddSign 加签（＝顺序会签，01a §4.3；`G2` **前置/后置由操作人当场选**）：
// 新任务并入**同 node_id**，`task_order` 由 `timing` 决定插入位：
//
//   - `AddSignAfter`（**默认**，缺省/空值）：`task_order = 本节点 max + 1` 追加到**队尾**；
//     原审批人**须继续等到其后所有人同意**（聚合按 node 全员）。
//   - `AddSignBefore`：`task_order = 当前办理人 order`，同节点 `order >= 该值` 者**整体 +1** →
//     **新增者先审**（插到当前办理人**之前**），当前办理人及其后让位（`RELEASED → HELD`）。
//
// ★ 两条共同约束：
//  1. **都不得使已 `APPROVED` 的任务重审**：前置插到的是**尚未审批**的「当前办理人」之前
//     （`order < 当前办理人 order` 的已通过者原样不动）。
//  2. **不变量（04a §2.3）必须成立**：任一时刻整实例「可办理」(`RELEASED ∧ PENDING`) 任务**恰 1 个**；
//     前置把可办理者由「当前办理人」**转为「新增者」**，**不是两个都可办理**。
//
// ★ 终态实例加签一律拒绝；`HELD` 任务、非本人一律拒绝。
func (s *Service) AddSign(ctx context.Context, bizNo, taskID, actorOpenID, targetOpenID, targetName, reason string, timing AddSignTiming) error {
	if timing == "" {
		timing = AddSignAfter // 缺省＝后置（保持既有调用方语义）
	}
	if timing != AddSignAfter && timing != AddSignBefore {
		return fmt.Errorf("%w: 非法加签时机 %q（仅 %s / %s）", ErrInvalidSubmit, timing, AddSignAfter, AddSignBefore)
	}
	at := time.Now()
	var events []FlowEvent
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		inst, err := s.db.GetInstanceByBizNoTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		if isTerminal(inst.Status) {
			return fmt.Errorf("%w: 实例 %s 已终态 %s，不可加签", ErrIllegalTransition, bizNo, inst.Status)
		}
		task, err := s.db.GetFlowTaskTx(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if task.BizNo != bizNo {
			return fmt.Errorf("%w: 任务 %s 不属于实例 %s", ErrIllegalTransition, taskID, bizNo)
		}
		// ★ 服务端硬校验（04a §5.1）：加签＝「操作者 ＝ assignee **且任务 `PENDING`**」。
		//   `PENDING` 是**独立前置条件**：仅校验 `RELEASED≠HELD` 不够 —— 已 `APPROVED` 的任务
		//   在顺序会签下**仍保持 `RELEASED`**（释放态单向、不随审批清理），故「已通过者」仍满足
		//   `RELEASED≠HELD`，若只看释放态会**放行"对已通过任务再次加签"**（语义比正本更宽）。
		//   ★ 「不变量成立」「不重审已通过者」**两个必要条件都不足以发现此缺口** —— 见 README 定案 #57。
		if task.Status != TaskPending {
			return fmt.Errorf("%w: 任务 %s 状态为 %s，不可加签（仅 PENDING 可加签，04a §5.1）",
				ErrIllegalTransition, taskID, task.Status)
		}
		// ★ N-060 F4（FR-M9-05）：加签次数上限 3（按单据累计）。★ 加签不开放代理人
		//（01a §4.1 表二：代理人不可加签）⇒ 此处保持**仅本人**、不接 authorizer。
		if n, cerr := s.db.CountFlowOpsByTypeTx(ctx, tx, bizNo, OpAddSign); cerr != nil {
			return cerr
		} else if n >= MaxAddSignPerInstance {
			return fmt.Errorf("%w: 加签已达上限 %d 次（FR-M9-05）", ErrOpLimitExceeded, MaxAddSignPerInstance)
		}
		if task.AssigneeOpenID != actorOpenID {
			return fmt.Errorf("%w: %s 非任务 %s 的审批人", ErrNotAssignee, actorOpenID, taskID)
		}
		if task.ReleaseState == ReleaseHeld {
			return fmt.Errorf("%w: 任务 %s（节点 %s）", ErrTaskHeld, taskID, task.NodeID)
		}
		if strings.TrimSpace(targetOpenID) == "" {
			return fmt.Errorf("%w: 加签目标为空", ErrInvalidSubmit)
		}

		nodeTasks, err := s.db.ListTasksByNodeTx(ctx, tx, bizNo, task.NodeID)
		if err != nil {
			return err
		}

		var (
			newOrder int
			yieldID  string // 前置：需让位（RELEASED→HELD）的「当前办理人」task_id
		)
		switch timing {
		case AddSignBefore:
			// 「当前办理人」＝同节点内**首个 PENDING** 任务（顺序会签不变量下即唯一「可办理」者）。
			cur := firstPendingTask(nodeTasks)
			if cur == nil {
				return fmt.Errorf("%w: 节点 %s 无待办理任务，无法前置加签", ErrIllegalTransition, task.NodeID)
			}
			newOrder = cur.TaskOrder
			yieldID = cur.TaskID
			// 当前办理人及其后整体 +1，腾出插入位（保持同节点 task_order 无重复、无空洞）。
			if _, err := s.db.ShiftFlowTaskOrderTx(ctx, tx, bizNo, task.NodeID, newOrder, 1); err != nil {
				return err
			}
		default: // AddSignAfter
			newOrder = maxTaskOrder(nodeTasks) + 1
		}

		newID, err := s.allocNodeTaskIDForOrderTx(ctx, tx, bizNo, task.NodeID, targetOpenID, task.Round, newOrder)
		if err != nil {
			return err
		}
		release := ReleaseHeld
		if timing == AddSignBefore {
			release = ReleaseReleased // 前置：新增者先审
		}
		if err := s.db.UpsertFlowTaskTx(ctx, tx, &store.FlowTask{
			TaskID: newID, BizNo: bizNo, NodeID: task.NodeID, NodeName: task.NodeName,
			NodeSeq: task.NodeSeq, Round: task.Round, AssigneeOpenID: targetOpenID, AssigneeName: targetName,
			Status: TaskPending, ReleaseState: release, TaskOrder: newOrder,
			CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			return err
		}
		// 前置：当前办理人让位（RELEASED → HELD），令「可办理」由新增者承接（不变量仍 = 1）。
		if yieldID != "" {
			if err := s.db.SetReleaseStateTx(ctx, tx, yieldID, ReleaseHeld); err != nil {
				return err
			}
		}
		if _, err := s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, NodeID: task.NodeID, TaskID: newID, OpType: OpAddSign,
			ActorOpenID: actorOpenID, FromStatus: "", ToStatus: TaskPending,
			Reason: reason, CreatedAt: at,
		}); err != nil {
			return err
		}
		// 保持「至多 1 个 RELEASED」不变量：
		//   前置已让位（新增者 RELEASED、当前办理人 HELD）→ 本调用为空转；
		//   后置下若原审批人已通过、新任务成为下一个待办 → 释放它。
		tasks, err := s.db.ListFlowTasksTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		if err := s.releaseNextTx(ctx, tx, bizNo, tasks); err != nil {
			return err
		}
		if err := s.saveInstanceTx(ctx, tx, inst, at); err != nil {
			return err
		}
		events = append(events, FlowEvent{
			Type: EventAddedSign, BizNo: bizNo, InstanceCode: inst.InstanceCode, DocType: inst.DocType,
			NodeID: task.NodeID, NodeName: task.NodeName, TaskID: newID, ActorOpenID: actorOpenID,
			Reason: reason, At: at,
		})
		return nil
	})
	if err != nil {
		return err
	}
	s.emit(ctx, events...)
	return nil
}

// Rollback 回退：回到**更早**的节点，被回退节点及其后节点整体置回 PENDING 且重置释放（`round+1`）。
//
// ★ 现状（01a §4.5）：**通知已通过者**；★ **驳回或撤销后流程全部重走**（本实现把 target 及其后节点整体重置）。
// ★ 只能回退到**严格更早**的节点（`targetSeq < activeSeq`）；对更晚/同节点回退＝非法。
func (s *Service) Rollback(ctx context.Context, bizNo, actorOpenID, targetNodeID, reason string) error {
	at := time.Now()
	var events []FlowEvent
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		inst, err := s.db.GetInstanceByBizNoTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		if isTerminal(inst.Status) {
			return fmt.Errorf("%w: 实例 %s 已终态 %s，不可回退", ErrIllegalTransition, bizNo, inst.Status)
		}
		tasks, err := s.db.ListFlowTasksTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		// ★ 准入收紧（`04a §5.1` / §5.2）：回退＝**「当前任务审批人本人」且任务 `PENDING`**。
		//   ＝ actor 须为**当前活动节点**（`maxReachedSeq`）的**当前 `PENDING` 任务** assignee。
		//   ★ 原判据 `actorHasTask`（「实例内**任一**任务持有者」）过宽：**上游节点已通过者**
		//     （其任务仍 `RELEASED`、`actorHasTask` 为真）也会被放行 → 比正本宽（fail-closed 修正）。
		// ★ N-060 F4（FR-M9-06）：回退次数上限 2（按单据累计；第 3 次可见拒绝）。
		if n, cerr := s.db.CountFlowOpsByTypeTx(ctx, tx, bizNo, OpRollback); cerr != nil {
			return cerr
		} else if n >= MaxRollbackPerInstance {
			return fmt.Errorf("%w: 回退已达上限 %d 次（FR-M9-06）", ErrOpLimitExceeded, MaxRollbackPerInstance)
		}
		activeSeq := maxReachedSeq(tasks)
		activeNodeID := nodeIDOfSeq(tasks, activeSeq)
		if activeNodeID == "" {
			return fmt.Errorf("%w: 实例 %s 无当前活动节点，不可回退", ErrIllegalTransition, bizNo)
		}
		activeTasks, err := s.db.ListTasksByNodeTx(ctx, tx, bizNo, activeNodeID)
		if err != nil {
			return err
		}
		cur := firstPendingTask(activeTasks)
		// ★ N-060 F4（FR-M9-06「本人或其代理人」）：非本人时经 authorizer 判代理
		//（三重判定见 Transfer 注释；nil ⇒ 仅本人）。
		if cur == nil || (cur.AssigneeOpenID != actorOpenID &&
			(s.agentAuthorizer == nil || !s.agentAuthorizer(ctx, cur, actorOpenID))) {
			return fmt.Errorf("%w: %s 非当前节点 %s 的当前审批人，不可回退（04a §5.1）",
				ErrNotAssignee, actorOpenID, activeNodeID)
		}
		targetSeq, ok := seqOfNode(tasks, targetNodeID)
		if !ok {
			return fmt.Errorf("%w: 节点 %s 不存在", ErrInvalidSubmit, targetNodeID)
		}
		if targetSeq >= activeSeq {
			return fmt.Errorf("%w: 只能回退到更早节点（target=%d, 当前=%d）",
				ErrIllegalTransition, targetSeq, activeSeq)
		}

		notifyTargets := ExpectedNotifyTargets(tasks, actorOpenID)

		// 被回退节点：整体 PENDING + 重置释放（首位 RELEASED，其余 HELD），round+1。
		targetTasks, err := s.db.ListTasksByNodeTx(ctx, tx, bizNo, targetNodeID)
		if err != nil {
			return err
		}
		for i, t := range targetTasks {
			release := ReleaseHeld
			if i == 0 {
				release = ReleaseReleased
			}
			if err := s.db.ResetFlowTaskTx(ctx, tx, t.TaskID, TaskPending, release, t.Round+1); err != nil {
				return err
			}
		}
		// 其后节点（"流程全部重走"）：整体 PENDING + 全部 HELD（逐级释放随推进发生），round+1。
		for _, t := range tasks {
			if t.NodeSeq <= targetSeq {
				continue
			}
			if err := s.db.ResetFlowTaskTx(ctx, tx, t.TaskID, TaskPending, ReleaseHeld, t.Round+1); err != nil {
				return err
			}
		}
		if _, err := s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, NodeID: targetNodeID, OpType: OpRollback,
			ActorOpenID: actorOpenID, ToStatus: InstancePending, Reason: reason, CreatedAt: at,
		}); err != nil {
			return err
		}
		if err := s.saveInstanceTx(ctx, tx, inst, at); err != nil {
			return err
		}
		events = append(events, FlowEvent{
			Type: EventRolledBack, BizNo: bizNo, InstanceCode: inst.InstanceCode, DocType: inst.DocType,
			NodeID: targetNodeID, ActorOpenID: actorOpenID, Reason: reason, At: at, NotifyTargets: notifyTargets,
		})
		return nil
	})
	if err != nil {
		return err
	}
	s.emit(ctx, events...)
	return nil
}

// ---------- 辅助 ----------

// allocNodeTaskIDTx 为某节点追加任务分配 task_id 与 task_order（**队尾**：本节点 max + 1），保证全库唯一。
//
// ★ 后置加签语义（`01a §4.3`）：新任务排到本节点**最末**（`后加者后审`）。
func (s *Service) allocNodeTaskIDTx(ctx context.Context, tx *sql.Tx, bizNo, nodeID, openID string, round int) (string, int, error) {
	nodeTasks, err := s.db.ListTasksByNodeTx(ctx, tx, bizNo, nodeID)
	if err != nil {
		return "", 0, err
	}
	order := maxTaskOrder(nodeTasks) + 1
	id, err := s.allocNodeTaskIDForOrderTx(ctx, tx, bizNo, nodeID, openID, round, order)
	if err != nil {
		return "", 0, err
	}
	return id, order, nil
}

// allocNodeTaskIDForOrderTx 在**指定 `task_order`** 处分配一个全库唯一的 `task_id`。
//
// ★ 与 `allocNodeTaskIDTx` 的区别：`task_order` 由调用方**显式给定**（前置加签＝插入位，
// 后置加签＝队尾），**绝不因 task_id 撞车而改变插入位**（改序＝静默改变"谁先审"）。
// 撞车时只对 task_id 追加消歧后缀，`order` 保持不变。
//
// `task_id` 形态（04a §3.3 确定性生成、含 `biz_no` 保全局唯一）：
// `{biz_no}-{node_id}-{open_id}-{round}-{order}`；撞车退化为尾缀 `-c{i}`。
func (s *Service) allocNodeTaskIDForOrderTx(ctx context.Context, tx *sql.Tx, bizNo, nodeID, openID string, round, order int) (string, error) {
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("%s-%s-%s-%d-%d", bizNo, nodeID, openID, round, order)
		if i > 0 {
			id = fmt.Sprintf("%s-%s-%s-%d-%d-c%d", bizNo, nodeID, openID, round, order, i)
		}
		if _, err := s.db.GetFlowTaskTx(ctx, tx, id); errors.Is(err, store.ErrNotFound) {
			return id, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("flow: 分配加签 task_id 失败（重试耗尽）")
}

// firstPendingTask 返回同节点任务中**首个 `PENDING`** 者（列表已按 `node_seq, task_order` 升序）。
//
// ★ 顺序会签不变量（04a §2.3）下，同节点任一时刻「可办理」(`RELEASED ∧ PENDING`) 恰 1 个，
// 即该节点**首个 `PENDING`** 任务 ＝「当前办理人」；`AddSignBefore` 即以它为插入锚点。
// 无 `PENDING`（节点已决/已终结）返回 nil。
func firstPendingTask(tasks []store.FlowTask) *store.FlowTask {
	for i := range tasks {
		if tasks[i].Status == TaskPending {
			return &tasks[i]
		}
	}
	return nil
}

// maxTaskOrder 返回同节点任务中最大的 `task_order`（无任务返回 0）。
// 「队尾」＝`maxTaskOrder + 1`（后置加签）。
func maxTaskOrder(tasks []store.FlowTask) int {
	max := 0
	for _, t := range tasks {
		if t.TaskOrder > max {
			max = t.TaskOrder
		}
	}
	return max
}

// seqOfNode 返回某 node_id 对应的 node_seq（找不到返回 false）。
func seqOfNode(tasks []store.FlowTask, nodeID string) (int, bool) {
	for _, t := range tasks {
		if t.NodeID == nodeID {
			return t.NodeSeq, true
		}
	}
	return 0, false
}

// maxReachedSeq 返回「已到达」节点中的最大 seq（＝当前活动节点）。
func maxReachedSeq(tasks []store.FlowTask) int {
	seqs := distinctSeqs(tasks)
	max := 0
	for _, seq := range seqs {
		if nodeReached(tasks, seq) && seq > max {
			max = seq
		}
	}
	return max
}
