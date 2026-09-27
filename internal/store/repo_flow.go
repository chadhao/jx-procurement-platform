package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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
	_, err := tx.ExecContext(ctx, `
INSERT INTO t_flow_task
  (task_id, biz_no, node_id, node_name, node_seq, round, assignee_open_id, assignee_name,
   status, action_context, created_at, updated_at, closed_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(task_id) DO NOTHING`,
		t.TaskID, t.BizNo, t.NodeID, nullStr(t.NodeName), t.NodeSeq, round,
		t.AssigneeOpenID, nullStr(t.AssigneeName), t.Status, nullStr(t.ActionContext),
		fmtTime(t.CreatedAt), fmtTime(t.UpdatedAt), nullStr(closed))
	if err != nil {
		return fmt.Errorf("store: 写入任务 %s 失败: %w", t.TaskID, err)
	}
	return nil
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

// ListFlowTasks 列出某业务单号的全部任务（按 node_seq、task_id 升序）。
func (d *DB) ListFlowTasks(ctx context.Context, bizNo string) ([]FlowTask, error) {
	rows, err := d.QueryContext(ctx, flowTaskSelectSQL+` WHERE biz_no = ? ORDER BY node_seq, task_id`, bizNo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectFlowTasks(rows)
}

// ListFlowTasksTx 在事务内列出某业务单号的全部任务。
func (d *DB) ListFlowTasksTx(ctx context.Context, tx *sql.Tx, bizNo string) ([]FlowTask, error) {
	rows, err := tx.QueryContext(ctx, flowTaskSelectSQL+` WHERE biz_no = ? ORDER BY node_seq, task_id`, bizNo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectFlowTasks(rows)
}

const flowTaskSelectSQL = `
SELECT task_id, biz_no, node_id, COALESCE(node_name,''), node_seq, COALESCE(round,1),
       assignee_open_id, COALESCE(assignee_name,''), status, COALESCE(action_context,''),
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
		created, updated string
		closed           string
	)
	if err := s.Scan(&t.TaskID, &t.BizNo, &t.NodeID, &t.NodeName, &t.NodeSeq, &t.Round,
		&t.AssigneeOpenID, &t.AssigneeName, &t.Status, &t.ActionContext,
		&created, &updated, &closed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
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
