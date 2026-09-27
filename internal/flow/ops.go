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

// ErrNotAssignee 操作者非该任务审批人（越权操作必须**可见地拒绝**）。
var ErrNotAssignee = errors.New("flow: 操作者非该任务审批人")

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
		if task.AssigneeOpenID != actorOpenID {
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

// AddSign 加签（＝顺序会签，01a §4.3）：新任务并入**同 node_id**、按 **task_order 插到队尾**。
//
// ★ 顺序会签：新任务先 `HELD`，**轮到才释放**；★ 原审批人**须继续等到其之后所有人同意**（聚合按 node 全员）。
// ★ `G2`（前置/后置）**待用户拍板** → 未定之前按**后置**实现（追加到队尾），并**保留 `node_seq` 扩展位**，
//
//	不自行前置。若将 G2 定为"前置"，改动点＝新任务 `task_order` 取当前首位两侧的插入点（本函数预留位置）。
func (s *Service) AddSign(ctx context.Context, bizNo, taskID, actorOpenID, targetOpenID, targetName, reason string) error {
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
		if task.AssigneeOpenID != actorOpenID {
			return fmt.Errorf("%w: %s 非任务 %s 的审批人", ErrNotAssignee, actorOpenID, taskID)
		}
		if task.ReleaseState == ReleaseHeld {
			return fmt.Errorf("%w: 任务 %s（节点 %s）", ErrTaskHeld, taskID, task.NodeID)
		}
		if strings.TrimSpace(targetOpenID) == "" {
			return fmt.Errorf("%w: 加签目标为空", ErrInvalidSubmit)
		}

		// 新任务并入同 node_id，按 task_order 插到队尾（后置，G2 未定）。
		newID, order, err := s.allocNodeTaskIDTx(ctx, tx, bizNo, task.NodeID, targetOpenID, task.Round)
		if err != nil {
			return err
		}
		if err := s.db.UpsertFlowTaskTx(ctx, tx, &store.FlowTask{
			TaskID: newID, BizNo: bizNo, NodeID: task.NodeID, NodeName: task.NodeName,
			NodeSeq: task.NodeSeq, Round: task.Round, AssigneeOpenID: targetOpenID, AssigneeName: targetName,
			Status: TaskPending, ReleaseState: ReleaseHeld, TaskOrder: order,
			CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			return err
		}
		if _, err := s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, NodeID: task.NodeID, TaskID: newID, OpType: OpAddSign,
			ActorOpenID: actorOpenID, FromStatus: "", ToStatus: TaskPending,
			Reason: reason, CreatedAt: at,
		}); err != nil {
			return err
		}
		// 保持「至多 1 个 RELEASED」不变量：若原审批人已通过、新任务成为下一个待办，则释放它。
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
		if !actorHasTask(tasks, actorOpenID) {
			return fmt.Errorf("%w: %s 非实例 %s 的审批人", ErrNotAssignee, actorOpenID, bizNo)
		}
		targetSeq, ok := seqOfNode(tasks, targetNodeID)
		if !ok {
			return fmt.Errorf("%w: 节点 %s 不存在", ErrInvalidSubmit, targetNodeID)
		}
		activeSeq := maxReachedSeq(tasks)
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

// allocNodeTaskIDTx 为某节点追加任务分配 task_id 与 task_order（队尾），保证全库唯一。
func (s *Service) allocNodeTaskIDTx(ctx context.Context, tx *sql.Tx, bizNo, nodeID, openID string, round int) (string, int, error) {
	nodeTasks, err := s.db.ListTasksByNodeTx(ctx, tx, bizNo, nodeID)
	if err != nil {
		return "", 0, err
	}
	order := 0
	for _, t := range nodeTasks {
		if t.TaskOrder > order {
			order = t.TaskOrder
		}
	}
	order++
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("%s-%s-%s-%d-%d", bizNo, nodeID, openID, round, order)
		if _, err := s.db.GetFlowTaskTx(ctx, tx, id); errors.Is(err, store.ErrNotFound) {
			return id, order, nil
		} else if err != nil {
			return "", 0, err
		}
		order++
	}
	return "", 0, fmt.Errorf("flow: 分配 task_id 失败（重试耗尽）")
}

// actorHasTask 判断某 open_id 是否拥有实例内至少一个任务（回退操作者校验）。
func actorHasTask(tasks []store.FlowTask, openID string) bool {
	for _, t := range tasks {
		if t.AssigneeOpenID == openID {
			return true
		}
	}
	return false
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
