// Package flow 是我方审批核心的领域服务（架构转向 ③，04a §2 / §11）。
//
// 职责：提交、节点聚合判定、推进；状态机为**唯一真相源**。
//
// ★ 会签聚合在**我方**（04a §2.3）：节点是否通过，按 `node_id` 聚合该节点**全部任务**的
//
//	状态判定，**不依赖飞书 `task_list[].type`**。`type` 只影响飞书侧展示形态。
//
// ★ 不可能状态防御（本仓库头号红线＝静默）：
//   - 终态（APPROVED/REJECTED/CANCELED）**不得回退**、不得再推进（重复回调为幂等 no-op）；
//   - CANCELED 后不得推进；
//   - 未到达的节点**不得提前审批**（防"未来节点先批"）。
package flow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/number"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 实例状态（04a §2.1）。
const (
	InstancePending  = "PENDING"
	InstanceApproved = "APPROVED"
	InstanceRejected = "REJECTED"
	InstanceCanceled = "CANCELED"
)

// 任务状态（04a §2.2）。
const (
	TaskPending     = "PENDING"
	TaskApproved    = "APPROVED"
	TaskRejected    = "REJECTED"
	TaskTransferred = "TRANSFERRED"
	TaskDone        = "DONE"
)

// 操作类型（04a §1.1）。
const (
	OpSubmit  = "SUBMIT"
	OpApprove = "APPROVE"
	OpReject  = "REJECT"
	OpCancel  = "CANCEL"
)

// 领域错误（均为**可见失败**，不静默吞掉）。
var (
	// ErrInvalidSubmit 提交入参非法。
	ErrInvalidSubmit = errors.New("flow: 提交入参非法")
	// ErrIllegalTransition 非法状态迁移（终态回退 / 已终结任务再操作等）。
	ErrIllegalTransition = errors.New("flow: 非法状态迁移")
	// ErrNodeNotReached 操作了尚未到达的节点。
	ErrNodeNotReached = errors.New("flow: 节点尚未到达")
)

// Approver 审批人。
type Approver struct {
	OpenID string
	Name   string
}

// NodeSpec 节点定义（提交时给出）。
type NodeSpec struct {
	NodeID    string
	NodeName  string
	Seq       int // 节点顺序（升序推进）
	Approvers []Approver
}

// SubmitInput 提交入参（我方页面校验通过后调用）。
type SubmitInput struct {
	DocType      string
	ApprovalCode string
	// PrevBizNo 重新发起时指向旧单号（撤回/驳回重提的因果链，04a §5.4）；首次提交留空。
	PrevBizNo       string
	ApplicantOpenID string
	ApplicantName   string
	Department      string
	AmountCents     *int64
	PurposeClassL1  string
	PurposeClassL2  string
	Supplier        string
	Nodes           []NodeSpec
	At              time.Time // 业务时刻（零值取 now），YYMM 由它决定
}

// Service 审批领域服务。
type Service struct {
	db    *store.DB
	gen   *number.Generator
	appID string
}

// New 构造审批服务。appID 用于生成 `instance_id = {app_id}:{biz_no}`（04a §3.3 / §6.3）。
func New(db *store.DB, appID string) *Service {
	return &Service{db: db, gen: number.New(db), appID: appID}
}

// instanceCode 由单号派生我方实例标识（= 推给飞书的 instance_id）。
//
// ★ 加 `{app_id}:` 前缀（04a §3.3）：裸单号在多环境/多应用共用同一应用时会**静默撞 ID**
//
//	（撞 ID = 审批中心看不到数据、无任何异常）。appID 为空时退化为裸单号（仅开发/测试）。
func (s *Service) instanceCode(bizNo string) string {
	if strings.TrimSpace(s.appID) == "" {
		return bizNo
	}
	return s.appID + ":" + bizNo
}

// Submit 提交：同事务内生成单号 + 建实例（PENDING）+ 建首节点任务 + 留痕。
// 返回业务单号。失败即回滚，不产生半成品实例。
func (s *Service) Submit(ctx context.Context, in SubmitInput) (string, error) {
	if err := validateSubmit(in); err != nil {
		return "", err
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}

	var bizNo string
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		bizNo, err = s.gen.AllocTx(ctx, tx, in.DocType, at)
		if err != nil {
			return err
		}
		inst := &store.Instance{
			InstanceCode:    s.instanceCode(bizNo),
			ApprovalCode:    in.ApprovalCode,
			DocType:         in.DocType,
			BizNo:           bizNo,
			PrevBizNo:       in.PrevBizNo,
			Status:          InstancePending,
			ApplicantOpenID: in.ApplicantOpenID,
			ApplicantName:   in.ApplicantName,
			Department:      in.Department,
			AmountCents:     in.AmountCents,
			PurposeClassL1:  in.PurposeClassL1,
			PurposeClassL2:  in.PurposeClassL2,
			Supplier:        in.Supplier,
			Source:          "flow",
			UpdateTime:      1, // 首次推送版本从 1 起（04a §3.1 必须严格递增）
			CreatedAt:       at,
			UpdatedAt:       at,
		}
		// ★ P3 兜底：biz_no 唯一索引拦截「试图复用终态号」——命中即失败，绝不静默分配同号。
		if err := s.db.UpsertInstanceTx(ctx, tx, inst); err != nil {
			return fmt.Errorf("flow: 提交写实例失败（单号唯一兜底）: %w", err)
		}
		if err := s.createTasksTx(ctx, tx, bizNo, in.Nodes, at); err != nil {
			return err
		}
		_, err = s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, OpType: OpSubmit, ActorOpenID: in.ApplicantOpenID,
			ToStatus: InstancePending, Reason: in.PrevBizNo, CreatedAt: at,
		})
		return err
	})
	if err != nil {
		return "", err
	}
	return bizNo, nil
}

// Approve 同意某任务（会签：需该节点全员同意才推进，04a §2.3）。
func (s *Service) Approve(ctx context.Context, bizNo, taskID, actorOpenID, reason string) error {
	return s.act(ctx, bizNo, taskID, actorOpenID, OpApprove, reason)
}

// Reject 拒绝某任务（节点驳回 → 实例驳回）。
func (s *Service) Reject(ctx context.Context, bizNo, taskID, actorOpenID, reason string) error {
	return s.act(ctx, bizNo, taskID, actorOpenID, OpReject, reason)
}

// Cancel 撤回（仅发起人；未终结前可撤）→ 实例 CANCELED、在途任务 DONE。
func (s *Service) Cancel(ctx context.Context, bizNo, actorOpenID, reason string) error {
	at := time.Now()
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		inst, err := s.db.GetInstanceByBizNoTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		if inst.Status == InstanceCanceled {
			return nil // 幂等：重复撤回 = no-op
		}
		if isTerminal(inst.Status) {
			return fmt.Errorf("%w: 实例 %s 已终态 %s，不可撤回", ErrIllegalTransition, bizNo, inst.Status)
		}
		if err := s.terminalizeTx(ctx, tx, inst, InstanceCanceled, at, reason); err != nil {
			return err
		}
		_, err = s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, OpType: OpCancel, ActorOpenID: actorOpenID,
			FromStatus: InstancePending, ToStatus: InstanceCanceled, Reason: reason, CreatedAt: at,
		})
		return err
	})
}

// act 是同意/拒绝的共用实现。
func (s *Service) act(ctx context.Context, bizNo, taskID, actor, opType, reason string) error {
	at := time.Now()
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		inst, err := s.db.GetInstanceByBizNoTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}

		task, err := s.db.GetFlowTaskTx(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if task.BizNo != bizNo {
			return fmt.Errorf("%w: 任务 %s 不属于实例 %s", ErrIllegalTransition, taskID, bizNo)
		}

		// ① 幂等：对已处于目标状态的任务重复操作 = no-op（04a §4.3 状态机层第二道防线）。
		if opType == OpApprove && task.Status == TaskApproved {
			return nil
		}
		if opType == OpReject && task.Status == TaskRejected {
			return nil
		}

		// ② 终态防御：实例已终态则不得再推进。
		//    对「同意/拒绝」这类回调，重复到达应为**幂等 no-op**（不二次迁移，不报错给飞书）；
		//    但实例状态本身绝不回退。
		if isTerminal(inst.Status) {
			return nil
		}

		// ③ 任务必须处于可操作态。
		if task.Status != TaskPending {
			return fmt.Errorf("%w: 任务 %s 状态为 %s，不可执行 %s",
				ErrIllegalTransition, taskID, task.Status, opType)
		}

		// ④ 节点到达校验：只有"当前节点"可操作（防未来节点提前审批这一不可能状态）。
		if err := s.ensureNodeReachedTx(ctx, tx, bizNo, task.NodeSeq); err != nil {
			return err
		}

		newTaskStatus := TaskApproved
		if opType == OpReject {
			newTaskStatus = TaskRejected
		}
		if err := s.db.UpdateFlowTaskStatusTx(ctx, tx, taskID, newTaskStatus, &at); err != nil {
			return err
		}
		if _, err := s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, NodeID: task.NodeID, TaskID: taskID, OpType: opType,
			ActorOpenID: actor, FromStatus: TaskPending, ToStatus: newTaskStatus,
			Reason: reason, CreatedAt: at,
		}); err != nil {
			return err
		}

		return s.advanceTx(ctx, tx, inst, at)
	})
}

// createTasksTx 建该实例全部节点的任务（PENDING）。
//
// ★ 为什么一次性建全链任务：`t_flow_task` 是唯一持久化"审批链"的地方（04a §1.1 无独立链路表），
//
//	推进时需据此判断"下一节点"。为避免后续节点被提前操作，`act` 用 `ensureNodeReachedTx`
//	做"只有当前节点可操作"的门禁（节点是否到达由更小 seq 的节点是否全部通过决定）。
func (s *Service) createTasksTx(ctx context.Context, tx *sql.Tx, bizNo string, nodes []NodeSpec, at time.Time) error {
	ordered := make([]NodeSpec, len(nodes))
	copy(ordered, nodes)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	idx := 0
	for _, n := range ordered {
		for _, ap := range n.Approvers {
			idx++
			// task_id 确定性生成（04a §3.3）：同快照重推得同一 ID，避免"审批中心看不到数据"。
			taskID := fmt.Sprintf("%s-%s-%d-%d", n.NodeID, ap.OpenID, 1, idx)
			t := &store.FlowTask{
				TaskID: taskID, BizNo: bizNo, NodeID: n.NodeID, NodeName: n.NodeName,
				NodeSeq: n.Seq, Round: 1, AssigneeOpenID: ap.OpenID, AssigneeName: ap.Name,
				Status: TaskPending, CreatedAt: at, UpdatedAt: at,
			}
			if err := s.db.UpsertFlowTaskTx(ctx, tx, t); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureNodeReachedTx 断言 seq 之前的所有节点均**已通过**，否则该节点尚未到达。
func (s *Service) ensureNodeReachedTx(ctx context.Context, tx *sql.Tx, bizNo string, seq int) error {
	tasks, err := s.db.ListFlowTasksTx(ctx, tx, bizNo)
	if err != nil {
		return err
	}
	for _, prev := range priorSeqs(tasks, seq) {
		if nodeDecision(tasks, prev) != nodeApproved {
			return fmt.Errorf("%w: 上游节点 seq=%d 未通过，节点 seq=%d 未到达", ErrNodeNotReached, prev, seq)
		}
	}
	return nil
}

// advanceTx 在任务状态变更后重算节点聚合与实例状态。
func (s *Service) advanceTx(ctx context.Context, tx *sql.Tx, inst *store.Instance, at time.Time) error {
	tasks, err := s.db.ListFlowTasksTx(ctx, tx, inst.BizNo)
	if err != nil {
		return err
	}

	// ① 任一任务被拒 → 实例驳回（"任一节点驳回"即终局，04a §2.1）。
	for _, t := range tasks {
		if t.Status == TaskRejected {
			return s.terminalizeTx(ctx, tx, inst, InstanceRejected, at, "")
		}
	}

	// ② 逐节点判定（按 seq 升序）：遇到未通过节点即停，不越过（"未推进"即"停在此节点"）。
	allApproved := true
	for _, seq := range distinctSeqs(tasks) {
		if nodeDecision(tasks, seq) == nodeApproved {
			continue
		}
		allApproved = false
		break
	}

	if allApproved {
		return s.terminalizeTx(ctx, tx, inst, InstanceApproved, at, "")
	}
	// 仍在流转：仅推进版本号（每次状态变更都要推一次，04a §3.1 / S2）。
	return s.saveInstanceTx(ctx, tx, inst, at)
}

// terminalizeTx 把实例置为终态，并被动终结其余在途任务。
func (s *Service) terminalizeTx(ctx context.Context, tx *sql.Tx, inst *store.Instance, status string, at time.Time, reason string) error {
	inst.Status = status
	if status == InstanceCanceled {
		inst.CancelReason = reason
		c := at
		inst.CancelAt = &c
	}
	// 终态：把仍未终结的任务置 DONE（被动终结，04a §2.2）。
	if err := s.closeOpenTasksTx(ctx, tx, inst.BizNo, at); err != nil {
		return err
	}
	return s.saveInstanceTx(ctx, tx, inst, at)
}

// closeOpenTasksTx 将该实例所有 PENDING 任务置 DONE。
func (s *Service) closeOpenTasksTx(ctx context.Context, tx *sql.Tx, bizNo string, at time.Time) error {
	tasks, err := s.db.ListFlowTasksTx(ctx, tx, bizNo)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Status == TaskPending {
			if err := s.db.UpdateFlowTaskStatusTx(ctx, tx, t.TaskID, TaskDone, &at); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveInstanceTx 持久化实例并令 update_time 单调 +1。
func (s *Service) saveInstanceTx(ctx context.Context, tx *sql.Tx, inst *store.Instance, at time.Time) error {
	inst.UpdatedAt = at
	inst.UpdateTime++
	return s.db.UpsertInstanceTx(ctx, tx, inst)
}

// ---------- 聚合与判定 ----------

// nodeState 节点聚合判定结果。
type nodeState int

const (
	nodePending  nodeState = iota // 未决（尚有 PENDING）
	nodeApproved                  // 通过（无 PENDING、无 REJECTED，且至少一个 APPROVED）
	nodeRejected                  // 驳回（存在任一 REJECTED）
)

// nodeDecision 对某个节点做**我方**聚合判定（会签语义，04a §2.3）。
//
// 规则：节点内任一任务 REJECTED → 驳回；否则当且仅当**无 PENDING** 且**至少一个 APPROVED**
// 时通过（TRANSFERRED/DONE 视为已终结但不提供"通过"）。空节点（无任务）视为未决，不误判通过。
func nodeDecision(tasks []store.FlowTask, seq int) nodeState {
	hasPending := false
	anyApproved := false
	hasTask := false
	for _, t := range tasks {
		if t.NodeSeq != seq {
			continue
		}
		hasTask = true
		switch t.Status {
		case TaskRejected:
			return nodeRejected
		case TaskPending:
			hasPending = true
		case TaskApproved:
			anyApproved = true
		}
	}
	if !hasTask || hasPending || !anyApproved {
		return nodePending
	}
	return nodeApproved
}

// distinctSeqs 返回任务中出现的全部 node_seq（升序去重）。
func distinctSeqs(tasks []store.FlowTask) []int {
	seen := map[int]bool{}
	var out []int
	for _, t := range tasks {
		if !seen[t.NodeSeq] {
			seen[t.NodeSeq] = true
			out = append(out, t.NodeSeq)
		}
	}
	sort.Ints(out)
	return out
}

// priorSeqs 返回严格小于 seq 的全部已出现 node_seq（升序）。
func priorSeqs(tasks []store.FlowTask, seq int) []int {
	var out []int
	for _, s := range distinctSeqs(tasks) {
		if s < seq {
			out = append(out, s)
		}
	}
	return out
}

// isTerminal 判断实例是否处于终态（04a §2.1）。
func isTerminal(status string) bool {
	switch status {
	case InstanceApproved, InstanceRejected, InstanceCanceled:
		return true
	default:
		return false
	}
}

// validateSubmit 校验提交入参。
func validateSubmit(in SubmitInput) error {
	if strings.TrimSpace(in.DocType) == "" {
		return fmt.Errorf("%w: doc_type 为空", ErrInvalidSubmit)
	}
	if len(in.Nodes) == 0 {
		return fmt.Errorf("%w: 审批链为空", ErrInvalidSubmit)
	}
	seenSeq := map[int]bool{}
	for _, n := range in.Nodes {
		if strings.TrimSpace(n.NodeID) == "" {
			return fmt.Errorf("%w: 节点 node_id 为空", ErrInvalidSubmit)
		}
		if len(n.Approvers) == 0 {
			return fmt.Errorf("%w: 节点 %s 无审批人", ErrInvalidSubmit, n.NodeID)
		}
		if seenSeq[n.Seq] {
			return fmt.Errorf("%w: 节点 seq=%d 重复", ErrInvalidSubmit, n.Seq)
		}
		seenSeq[n.Seq] = true
		for _, ap := range n.Approvers {
			if strings.TrimSpace(ap.OpenID) == "" {
				return fmt.Errorf("%w: 节点 %s 存在空审批人 open_id", ErrInvalidSubmit, n.NodeID)
			}
		}
	}
	return nil
}
