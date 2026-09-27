package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---------- 通知流水（t_notify_log，04a §1.1 / §5.5；漏发可检出） ----------
//
// ★ 两阶段（01a §4.7 / 04a §5.5）：**先落 EXPECTED（应有集合），再由发送结果回填 SENT/FAILED**。
//
//	漏发判定＝「EXPECTED 中未变为 SENT 的记录」——**必须靠落盘的 EXPECTED**，
//	★ **不能用"实时算"判漏发**（实时算检不出"该发没发"：算出来的集合永远自洽）。
//
// 通知行状态：PENDING（保留旧值）/ EXPECTED（应有、尚未发）/ SENT / FAILED。
const (
	NotifyExpected = "EXPECTED"
	NotifySent     = "SENT"
	NotifyFailed   = "FAILED"
)

const notifySelectSQL = `
SELECT id, biz_no, target_open_id, channel, event, status, COALESCE(attempts,0),
       COALESCE(last_error,''), COALESCE(sent_at,''), created_at
FROM t_notify_log`

// EnsureNotifyExpected 幂等登记一条「应有通知」（key＝biz_no+target+event+channel）。
//
// ★ 仅当不存在时插入（**不把已 SENT 的行重置回 EXPECTED**）：否则重放会把"已发出"错改成"待发"，
// 造成漏发误报（与本项目"静默"缺陷族对称的"误报"缺陷）。
func (d *DB) EnsureNotifyExpected(ctx context.Context, n *NotifyLog) error {
	if n == nil || n.BizNo == "" || n.TargetOpenID == "" || n.Event == "" {
		return fmt.Errorf("store: 登记通知失败: biz_no/target_open_id/event 不能为空")
	}
	var existing int64
	err := d.QueryRowContext(ctx, `
SELECT id FROM t_notify_log WHERE biz_no = ? AND target_open_id = ? AND event = ? AND channel = ? LIMIT 1`,
		n.BizNo, n.TargetOpenID, n.Event, n.Channel).Scan(&existing)
	switch {
	case err == nil:
		return nil // 已存在：不覆盖
	case errors.Is(err, sql.ErrNoRows):
		// 不存在 → 插入 EXPECTED
	default:
		return fmt.Errorf("store: 查询通知失败: %w", err)
	}
	status := strings.TrimSpace(n.Status)
	if status == "" {
		status = NotifyExpected
	}
	_, err = d.ExecContext(ctx, `
INSERT INTO t_notify_log
  (biz_no, target_open_id, channel, event, status, attempts, created_at)
VALUES (?,?,?,?,?,?,?)`,
		n.BizNo, n.TargetOpenID, n.Channel, n.Event, status, n.Attempts, fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 登记通知失败: %w", err)
	}
	return nil
}

// UpdateNotifyStatus 回填通知发送结果（按 biz_no+target+event+channel）。
func (d *DB) UpdateNotifyStatus(ctx context.Context, bizNo, target, event, channel, status, lastErr string) error {
	var sent any
	if status == NotifySent {
		sent = fmtTime(timeNow().UTC())
	}
	res, err := d.ExecContext(ctx, `
UPDATE t_notify_log SET status = ?, last_error = ?, attempts = attempts + 1, sent_at = ?
WHERE biz_no = ? AND target_open_id = ? AND event = ? AND channel = ?`,
		status, nullStr(lastErr), sent, bizNo, target, event, channel)
	if err != nil {
		return fmt.Errorf("store: 回填通知状态失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: 回填通知状态失败: %w", ErrNotFound)
	}
	return nil
}

// ListNotify 列出某业务单号的全部通知（按 id 升序）。
func (d *DB) ListNotify(ctx context.Context, bizNo string) ([]NotifyLog, error) {
	rows, err := d.QueryContext(ctx, notifySelectSQL+` WHERE biz_no = ? ORDER BY id`, bizNo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectNotify(rows)
}

// ListUnsentNotify 列出某业务单号**未成功发送**的通知（status ≠ SENT）——漏发检出用。
func (d *DB) ListUnsentNotify(ctx context.Context, bizNo string) ([]NotifyLog, error) {
	rows, err := d.QueryContext(ctx,
		notifySelectSQL+` WHERE biz_no = ? AND status <> ? ORDER BY id`, bizNo, NotifySent)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectNotify(rows)
}

// CountNotifyByStatus 统计某业务单号下指定状态的通知条数（自检 / 用例断言）。
func (d *DB) CountNotifyByStatus(ctx context.Context, bizNo, status string) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_notify_log WHERE biz_no = ? AND status = ?`, bizNo, status).Scan(&n)
	return n, err
}

func collectNotify(rows *sql.Rows) ([]NotifyLog, error) {
	var out []NotifyLog
	for rows.Next() {
		var (
			n       NotifyLog
			sent    string
			created string
		)
		if err := rows.Scan(&n.ID, &n.BizNo, &n.TargetOpenID, &n.Channel, &n.Event, &n.Status,
			&n.Attempts, &n.LastError, &sent, &created); err != nil {
			return nil, err
		}
		n.SentAt = parseTimePtr(sent)
		n.CreatedAt = parseTime(created)
		out = append(out, n)
	}
	return out, rows.Err()
}
