package flow

import (
	"context"
	"errors"
	"fmt"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// repair.go —— 派生式修复循环（#69 ②）：把「已落盘、未推进」的回调**重驱动**到推进完成。
//
// ★ 为什么需要它（缺它则 #69 ① 有害）：#69 ① 让回调「**落盘即 200**」。此后若异步推进 `act`
//   失败，回调**不会被重试**（飞书收到 200；且重发也因幂等键判 Duplicate）→ 任务**静默卡在 PENDING**。
//   本循环即该形态的**兜底**：定期扫出这些行并重驱动。
//
// ★ 判据与幂等：
//   - 「卡住」＝`t_flow_op_log` 有 APPROVE/REJECT 留痕、但对应任务仍 PENDING（**同 round**、
//     **已释放**、实例**非终态**）—— 完整 SQL 与逐条理由见 `store.ListStuckApprovalOps`。
//   - 重驱动走**既有 `act`**：它自带 `operator==assignee`、幂等（已是目标态→no-op）、
//     终态防御、节点到达、释放门禁 —— 故**无需在本循环重造任何状态机语义**（避免"两套口径"）。
//   - ★ 幂等（连跑不双推进）：重驱动成功后任务不再是 PENDING → **下一轮扫描自然不再选中它**；
//     即便同轮并发，`act` 的「已是目标态→no-op」守卫也保证只推进一次。
//
// ★ 装配：由 `cmd/jxapproval/bootstrap.go` 起一个 ticker 调用本方法（启动先跑一次做 catch-up）。

// RepairReport 一次修复扫描的结果（供日志与探针断言）。
type RepairReport struct {
	Scanned  int      // 命中「有留痕但未推进」的行数
	Repaired int      // 成功重驱动并推进的行数
	Skipped  int      // 被状态机守卫挡回（未轮到 / 节点未到 / 越权 / 非法迁移）＝无需推进，非失败
	Failed   int      // 重驱动报错的行数
	Errors   []string // 失败明细（`biz_no/task_id: err`）
	// Failures 「最终失败」行的结构化明细（第 3 批新增，docs/16 §2-F-③）：
	// 供装配层对每行调 message/update 标注飞书卡片（用落盘的 message_id）。
	// ★ 只增不改：Errors 字符串明细保留（既有日志/断言消费方不变）。
	Failures []RepairFailure
}

// RepairFailure 一行「重驱动失败」的结构化留痕（F：卡片失败反馈的数据来源）。
type RepairFailure struct {
	Op  store.StuckApprovalOp // 卡住的行（含落盘的 message_id，可能为空）
	Err error                 // 重驱动失败原因
}

// RepairPendingApprovals 扫出「已落盘、未推进」的回调并重驱动之，返回本次结果。
//
// ★ 只做「查 + 重驱动」：查询是只读（`store.ListStuckApprovalOps`），写一律经 `act`（幂等出口）。
//
//	DB 扫描失败时返回 error（调用方应记日志、下一轮重试）；单行重驱动失败则计入 Report.Failed
//	而**不**中断整轮（一行坏数据不得让其余行永远得不到修复）。
func (s *Service) RepairPendingApprovals(ctx context.Context) (RepairReport, error) {
	ops, err := s.db.ListStuckApprovalOps(ctx)
	if err != nil {
		return RepairReport{}, err
	}
	rep := RepairReport{Scanned: len(ops)}
	for _, op := range ops {
		var derr error
		switch op.OpType {
		case OpApprove:
			derr = s.act(ctx, op.BizNo, op.TaskID, op.ActorOpenID, OpApprove, repairReason)
		case OpReject:
			derr = s.act(ctx, op.BizNo, op.TaskID, op.ActorOpenID, OpReject, repairReason)
		default:
			continue // 查询已限 APPROVE/REJECT；防御性跳过其余 op。
		}
		switch {
		case derr == nil:
			rep.Repaired++
		case errors.Is(derr, ErrIllegalTransition),
			errors.Is(derr, ErrTaskHeld),
			errors.Is(derr, ErrNodeNotReached),
			errors.Is(derr, ErrNotAssignee):
			// 「被状态机清晰挡回」＝当前**无需/不可**推进（未轮到、节点未到、越权、非法迁移）。
			// 非失败：记 Skipped 即可；若条件后续变化，下一轮扫描会自然重试。
			rep.Skipped++
		default:
			rep.Failed++
			rep.Errors = append(rep.Errors,
				fmt.Sprintf("%s/%s: %v", op.BizNo, op.TaskID, derr))
			// ★ 第 3 批（docs/16 §2-F-③）：结构化留痕供装配层做卡片失败反馈
			// （message_id 为空时由反馈器拦截，不发同步请求）。
			rep.Failures = append(rep.Failures, RepairFailure{Op: op, Err: derr})
		}
	}
	return rep, nil
}

// repairReason 修复循环写入 op_log.reason 的标记（便于与真实回调留痕区分）。
const repairReason = "repair: 派生式重驱动（#69）"
