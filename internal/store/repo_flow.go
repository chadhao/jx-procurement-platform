package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 任务释放状态（0008，04a §2.3）。字符串与 flow 包的 ReleaseHeld/ReleaseReleased 一致。
const (
	releaseHeld     = "HELD"
	releaseReleased = "RELEASED"
)

// ---------- 我方任务 / 节点（t_flow_task，04a §1.1） ----------

// UpsertFlowTaskTx 写入任务行（task_id 唯一）。
//
// ★ 冲突即 no-op（DO NOTHING）：task_id 为**确定性**生成，一旦提交即固定；
// 若允许冲突覆盖，会把已推进的 status 重置回 PENDING —— 这是「被撞回起点」的静默缺陷。
// 任务状态变更一律走 UpdateFlowTaskStatusTx。
func (d *DB) UpsertFlowTaskTx(ctx context.Context, tx *sql.Tx, t *FlowTask) error {
	if t == nil || t.TaskID == "" || t.BizNo == "" {
		return fmt.Errorf("store: 写入任务失败: task_id/biz_no 不能为空")
	}
	round := t.Round
	if round <= 0 {
		round = 1
	}
	closed := ""
	if t.ClosedAt != nil {
		closed = fmtTime(*t.ClosedAt)
	}
	// ★ 释放状态：调用方一般显式给出；缺省按「已释放」写入（＝旧语义，避免漏写即静默冻结）。
	release := strings.ToUpper(strings.TrimSpace(t.ReleaseState))
	if release != releaseHeld && release != releaseReleased {
		release = releaseReleased
	}
	order := t.TaskOrder
	if order < 0 {
		order = 0
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO t_flow_task
  (task_id, biz_no, node_id, node_name, node_seq, round, assignee_open_id, assignee_name,
   status, action_context, release_state, weight, task_order, created_at, updated_at, closed_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(task_id) DO NOTHING`,
		t.TaskID, t.BizNo, t.NodeID, nullStr(t.NodeName), t.NodeSeq, round,
		t.AssigneeOpenID, nullStr(t.AssigneeName), t.Status, nullStr(t.ActionContext),
		release, t.Weight, order,
		fmtTime(t.CreatedAt), fmtTime(t.UpdatedAt), nullStr(closed))
	if err != nil {
		return fmt.Errorf("store: 写入任务 %s 失败: %w", t.TaskID, err)
	}
	return nil
}

// SetReleaseStateTx 设置任务释放状态（顺序会签逐级释放，04a §2.3）。
//
// ★ 单向纪律（防「被撞回起点」的静默缺陷）：
//   - 本方法**只用于把 HELD 任务释放为 RELEASED**（`RELEASED` 分支带 `WHERE release_state='HELD'`
//     守卫，重复释放＝幂等 no-op，绝不改动已释放/已终结任务）；
//   - `HELD` 分支保留给未来「回退后重置释放」（04a §2.3 末行）；本期无调用点，仅提供能力。
//
// 返回 error 仅在 DB 失败时非 nil；「无行受影响」在 RELEASED 分支属正常（已释放/已终结），不报错。
func (d *DB) SetReleaseStateTx(ctx context.Context, tx *sql.Tx, taskID, state string) error {
	state = strings.ToUpper(strings.TrimSpace(state))
	if state != releaseHeld && state != releaseReleased {
		return fmt.Errorf("store: 非法 release_state %q（仅 HELD/RELEASED）", state)
	}
	now := fmtTime(timeNow().UTC())
	if state == releaseReleased {
		// 单向：仅 HELD → RELEASED。
		if _, err := tx.ExecContext(ctx, `
UPDATE t_flow_task SET release_state = ?, updated_at = ?
WHERE task_id = ? AND release_state = ?`, releaseReleased, now, taskID, releaseHeld); err != nil {
			return fmt.Errorf("store: 释放任务 %s 失败: %w", taskID, err)
		}
		return nil
	}
	// HELD：仅供未来「回退重置释放」使用。
	if _, err := tx.ExecContext(ctx, `
UPDATE t_flow_task SET release_state = ?, updated_at = ?
WHERE task_id = ?`, releaseHeld, now, taskID); err != nil {
		return fmt.Errorf("store: 重置任务 %s 释放状态失败: %w", taskID, err)
	}
	return nil
}

// ResetFlowTaskTx 重置任务的 状态 / 释放状态 / 轮次（回退「按顺序位重放」用，04a §2.3 末行）。
//
// ★ 与 UpdateFlowTaskStatusTx 的区别：本方法一并重置 `release_state` 与 `round`、并清空 `closed_at`，
// 用于"把被回退节点整体置回 PENDING 且重置释放"；**历史不删除**（op_log 另行追加）。
func (d *DB) ResetFlowTaskTx(ctx context.Context, tx *sql.Tx, taskID, status, releaseState string, round int) error {
	releaseState = strings.ToUpper(strings.TrimSpace(releaseState))
	if releaseState != releaseHeld && releaseState != releaseReleased {
		releaseState = releaseHeld
	}
	if round <= 0 {
		round = 1
	}
	res, err := tx.ExecContext(ctx, `
UPDATE t_flow_task SET status = ?, release_state = ?, round = ?, closed_at = NULL, updated_at = ?
WHERE task_id = ?`,
		status, releaseState, round, fmtTime(timeNow().UTC()), taskID)
	if err != nil {
		return fmt.Errorf("store: 重置任务 %s 失败: %w", taskID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: 重置任务 %s 失败: %w", taskID, ErrNotFound)
	}
	return nil
}

// ListTasksByNodeTx 在事务内列出某实例某节点下的全部任务，按**释放顺序**升序。
//
// ★ 排序键＝`node_seq, task_order`（显式契约列）：04a §2.3 要求「同 node_id 按次序逐级释放」，
//
//	但本仓库 `node_seq` 语义＝**节点顺序**（同一节点内各任务 node_seq 相同），故同节点内
//	以 `task_order`（＝审批人声明序，1-based，createTasksTx 显式写入）作次序键。
//	★ 禁止改用 `rowid`（随 REPLACE/VACUUM 漂移）或 `task_id`（含 open_id，字典序≠声明序）。
func (d *DB) ListTasksByNodeTx(ctx context.Context, tx *sql.Tx, bizNo, nodeID string) ([]FlowTask, error) {
	rows, err := tx.QueryContext(ctx,
		flowTaskSelectSQL+` WHERE biz_no = ? AND node_id = ? ORDER BY node_seq, task_order`, bizNo, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectFlowTasks(rows)
}

// ShiftFlowTaskOrderTx 把某实例某节点内 `task_order >= fromOrder` 的任务**整体平移** delta。
//
// ★ 用途＝**前置加签腾位**（`01a §4.3` 加签时机 · 前置）：在「当前办理人 order」处插入新任务前，
// 把该位及其后所有任务的 `task_order` 整体 +1，腾出插入位 → 平移后同节点 `task_order` 仍**无重复、无空洞**。
//
// ★ 契约：`task_order` 是同节点**唯一顺序键**（迁移 0009）；本方法**不依赖 `rowid` / `task_id`**。
//
// ★ 为什么可安全整体平移：迁移 0009 的 `task_order` **无 UNIQUE 约束**（仅 `NOT NULL DEFAULT 0`），
// 故 `UPDATE … SET task_order = task_order + 1` 不会触发唯一冲突（默认走一次全表扫描的 UPDATE，逐行自增无碰撞）。
//
// ★ **只改 `task_order`**：绝不动 `status` / `release_state` / `round` —— 平移**不得重审已 `APPROVED` 者**、
// 也**不得改动释放态**（释放态由调用方按「让位」语义单独设置）。
//
// 返回受影响行数（供调用方断言「确有多行被平移」）。
func (d *DB) ShiftFlowTaskOrderTx(ctx context.Context, tx *sql.Tx, bizNo, nodeID string, fromOrder, delta int) (int64, error) {
	if delta == 0 {
		return 0, nil
	}
	res, err := tx.ExecContext(ctx, `
UPDATE t_flow_task SET task_order = task_order + ?, updated_at = ?
WHERE biz_no = ? AND node_id = ? AND task_order >= ?`,
		delta, fmtTime(timeNow().UTC()), bizNo, nodeID, fromOrder)
	if err != nil {
		return 0, fmt.Errorf("store: 平移节点 %s 任务次序失败: %w", nodeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// UpdateFlowTaskStatusTx 更新任务状态（+ updated_at / closed_at）。
func (d *DB) UpdateFlowTaskStatusTx(ctx context.Context, tx *sql.Tx, taskID, status string, closedAt *time.Time) error {
	closed := ""
	if closedAt != nil {
		closed = fmtTime(*closedAt)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE t_flow_task SET status = ?, updated_at = ?, closed_at = ? WHERE task_id = ?`,
		status, fmtTime(timeNow().UTC()), nullStr(closed), taskID)
	if err != nil {
		return fmt.Errorf("store: 更新任务 %s 失败: %w", taskID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: 更新任务 %s 失败: %w", taskID, ErrNotFound)
	}
	return nil
}

// GetFlowTaskTx 在事务内按 task_id 读取任务；不存在返回 ErrNotFound。
func (d *DB) GetFlowTaskTx(ctx context.Context, tx *sql.Tx, taskID string) (*FlowTask, error) {
	row := tx.QueryRowContext(ctx, flowTaskSelectSQL+` WHERE task_id = ?`, taskID)
	return scanFlowTask(row)
}

// GetFlowTask 按 task_id 读取任务；不存在返回 ErrNotFound。
func (d *DB) GetFlowTask(ctx context.Context, taskID string) (*FlowTask, error) {
	row := d.QueryRowContext(ctx, flowTaskSelectSQL+` WHERE task_id = ?`, taskID)
	return scanFlowTask(row)
}

// ListFlowTasks 列出某业务单号的全部任务（按 node_seq、task_order 升序）。
//
// ★ 次序键＝`node_seq, task_order`（都是显式契约列）：节点间以 node_seq，同节点内以
//
//	task_order（审批人声明序）排序。★ 禁止改用 `rowid` 或 `task_id` 排序。
func (d *DB) ListFlowTasks(ctx context.Context, bizNo string) ([]FlowTask, error) {
	rows, err := d.QueryContext(ctx, flowTaskSelectSQL+` WHERE biz_no = ? ORDER BY node_seq, task_order`, bizNo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectFlowTasks(rows)
}

// ListFlowTasksTx 在事务内列出某业务单号的全部任务（序列同 ListFlowTasks）。
func (d *DB) ListFlowTasksTx(ctx context.Context, tx *sql.Tx, bizNo string) ([]FlowTask, error) {
	rows, err := tx.QueryContext(ctx, flowTaskSelectSQL+` WHERE biz_no = ? ORDER BY node_seq, task_order`, bizNo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectFlowTasks(rows)
}

const flowTaskSelectSQL = `
SELECT task_id, biz_no, node_id, COALESCE(node_name,''), node_seq, COALESCE(round,1),
       assignee_open_id, COALESCE(assignee_name,''), status, COALESCE(action_context,''),
       COALESCE(release_state,'HELD'), weight, COALESCE(task_order,0),
       created_at, updated_at, COALESCE(closed_at,'')
FROM t_flow_task`

func collectFlowTasks(rows *sql.Rows) ([]FlowTask, error) {
	var out []FlowTask
	for rows.Next() {
		t, err := scanFlowTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func scanFlowTask(s interface {
	Scan(dest ...any) error
}) (*FlowTask, error) {
	var (
		t                FlowTask
		weight           sql.NullInt64
		created, updated string
		closed           string
	)
	if err := s.Scan(&t.TaskID, &t.BizNo, &t.NodeID, &t.NodeName, &t.NodeSeq, &t.Round,
		&t.AssigneeOpenID, &t.AssigneeName, &t.Status, &t.ActionContext,
		&t.ReleaseState, &weight, &t.TaskOrder,
		&created, &updated, &closed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if weight.Valid {
		v := int(weight.Int64)
		t.Weight = &v
	}
	t.CreatedAt = parseTime(created)
	t.UpdatedAt = parseTime(updated)
	t.ClosedAt = parseTimePtr(closed)
	return &t, nil
}

// ---------- 操作留痕（t_flow_op_log，04a §1.1 / §4.3） ----------

// InsertFlowOpLogTx 追加一条操作留痕。
//
// ★ 幂等（04a §4.3）：对 APPROVE/REJECT 存在 (biz_no,task_id,op_type) 部分唯一索引，
// 重复回调在此被 `INSERT OR IGNORE` 吸收（返回 inserted=false）→ **不产生第二次迁移**。
// 其余 op_type 无该约束，正常插入。
//
// 返回 inserted 表示本次是否真的写入（false＝幂等命中）。
func (d *DB) InsertFlowOpLogTx(ctx context.Context, tx *sql.Tx, op *FlowOpLog) (bool, error) {
	if op == nil || op.BizNo == "" || op.OpType == "" {
		return false, fmt.Errorf("store: 写入操作留痕失败: biz_no/op_type 不能为空")
	}
	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO t_flow_op_log
  (biz_no, node_id, task_id, op_type, actor_open_id, from_status, to_status, reason, extra_json, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?)`,
		op.BizNo, nullStr(op.NodeID), nullStr(op.TaskID), op.OpType, nullStr(op.ActorOpenID),
		nullStr(op.FromStatus), nullStr(op.ToStatus), nullStr(op.Reason), nullStr(op.ExtraJSON),
		fmtTime(op.CreatedAt))
	if err != nil {
		return false, fmt.Errorf("store: 写入操作留痕失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListFlowOpLogs 列出某业务单号的操作留痕（按 op_id 升序）。
func (d *DB) ListFlowOpLogs(ctx context.Context, bizNo string) ([]FlowOpLog, error) {
	rows, err := d.QueryContext(ctx, `
SELECT op_id, biz_no, COALESCE(node_id,''), COALESCE(task_id,''), op_type,
       COALESCE(actor_open_id,''), COALESCE(from_status,''), COALESCE(to_status,''),
       COALESCE(reason,''), COALESCE(extra_json,''), created_at
FROM t_flow_op_log WHERE biz_no = ? ORDER BY op_id`, bizNo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []FlowOpLog
	for rows.Next() {
		var (
			op      FlowOpLog
			created string
		)
		if err := rows.Scan(&op.OpID, &op.BizNo, &op.NodeID, &op.TaskID, &op.OpType, &op.ActorOpenID,
			&op.FromStatus, &op.ToStatus, &op.Reason, &op.ExtraJSON, &created); err != nil {
			return nil, err
		}
		op.CreatedAt = parseTime(created)
		out = append(out, op)
	}
	return out, rows.Err()
}
