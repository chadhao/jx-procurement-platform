package store

import (
	"context"
	"fmt"
)

// repo_flow_repair.go —— 派生式修复循环的**只读扫描**（#69 ②；SQL 留在 store 层）。
//
// ★ 背景（#69 ① 的必然配套）：回调同步路径「**落盘即 200**」——只要 `t_flow_op_log` 已写入
//
//	APPROVE/REJECT 留痕，HTTP 就回 200（用户口径 D2）。此后若 `act`（异步推进）**失败**，
//	该回调**不会再被重试**（飞书收到 200、且重发也会因幂等键判 `Duplicate`）→ 任务**静默卡在 PENDING**。
//
// ★ 本查询＝「把卡住的行找出来」：**op_log 有 APPROVE/REJECT 留痕，但对应任务仍是 PENDING**。
//
//	判据必须**逐条对齐**（任一放宽都会重驱动不该动的行，或漏掉该动的行）：
//	  · `o.op_type IN ('APPROVE','REJECT')` —— 只认两键（其余 op 无「同步落盘 + 异步推进」两段式）；
//	  · `o.task_id = t.task_id` 且 `t.biz_no = o.biz_no` —— 留痕必须**指向同一个任务**；
//	  · ★★ `o.round = t.round` —— **按轮次对齐**（0011 新加的列）：
//	      任务被回退（Rollback）后 `round +1` 且**复用同一 `task_id`**；若不比 round，
//	      上一轮的旧 APPROVE 留痕会与「回退后的 PENDING 任务」错配 → **把新轮次误判为待推进**。
//	      这正是 `0011`「round 入键」能力在本循环的用途。
//	  · `t.status = 'PENDING'` —— 只有**尚未推进**的任务才需重驱动（已 APPROVED/REJECTED 的，留痕已兑现）；
//	  · ★ `t.release_state = 'RELEASED'` —— **HELD 不动**（顺序会签下「还没轮到」，重驱动只会白跑；
//	      待其被释放后，下一轮扫描自然纳入）；
//	  · `i.status NOT IN ('APPROVED','REJECTED','CANCELED')` —— 实例**非终态**（终态下任务多已 DONE，
//	      且即便残留也不得再推进）。
//
// ★ 只读：本方法**不写任何表**；重驱动（写）由 `flow.RepairPendingApprovals` 经既有 `act` 完成
//
//	（`act` 自带幂等守卫）。
func (d *DB) ListStuckApprovalOps(ctx context.Context) ([]StuckApprovalOp, error) {
	rows, err := d.QueryContext(ctx, `
SELECT o.biz_no, o.task_id, o.op_type, COALESCE(o.round, 0), COALESCE(o.actor_open_id, '')
FROM t_flow_op_log o
JOIN t_flow_task t ON t.task_id = o.task_id AND t.biz_no = o.biz_no
JOIN t_instance  i ON i.biz_no  = o.biz_no
WHERE o.op_type IN ('APPROVE', 'REJECT')
  AND o.task_id IS NOT NULL AND o.task_id <> ''
  AND COALESCE(o.round, 0) = COALESCE(t.round, 1)
  AND t.status = 'PENDING'
  AND COALESCE(t.release_state, 'HELD') = 'RELEASED'
  AND i.status NOT IN ('APPROVED', 'REJECTED', 'CANCELED')
ORDER BY o.op_id`)
	if err != nil {
		return nil, fmt.Errorf("store: 扫描待修复审批留痕失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []StuckApprovalOp
	for rows.Next() {
		var s StuckApprovalOp
		var round int64
		if err := rows.Scan(&s.BizNo, &s.TaskID, &s.OpType, &round, &s.ActorOpenID); err != nil {
			return nil, fmt.Errorf("store: 解析待修复审批留痕失败: %w", err)
		}
		s.Round = int(round)
		out = append(out, s)
	}
	return out, rows.Err()
}

// StuckApprovalOp 一行「已留痕但任务未推进」的待修复操作（= ListStuckApprovalOps 的元素）。
type StuckApprovalOp struct {
	BizNo       string
	TaskID      string
	OpType      string // APPROVE / REJECT
	Round       int    // op_log.round（与 task.round 对齐）
	ActorOpenID string // op_log 记录的操作人（准入时已校验 = 任务 assignee）
}
