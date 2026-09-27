package store

import (
	"context"
	"fmt"
	"strings"
)

// ---------- 推送流水（t_push_record，04a §1.1 / §3 / §10 S4） ----------
//
// 每次出方向推送（external_instances）写一行：push_seq＝推送时的 update_time（逻辑版本）。
// (biz_no, push_seq) 唯一 → 同一版本重复推送不产生重复行（幂等，见 0007 迁移）。
const (
	PushPending = "PENDING"
	PushSent    = "SENT"
	PushFailed  = "FAILED"
)

const pushRecordSelectSQL = `
SELECT id, biz_no, push_seq, COALESCE(snapshot_hash,''), status, COALESCE(attempts,0),
       COALESCE(last_error,''), created_at, COALESCE(sent_at,'')
FROM t_push_record`

// InsertPushRecord 幂等写入推送流水（(biz_no,push_seq) 冲突即忽略）。
func (d *DB) InsertPushRecord(ctx context.Context, r *PushRecord) error {
	if r == nil || r.BizNo == "" || r.PushSeq <= 0 {
		return fmt.Errorf("store: 写入推送流水失败: biz_no/push_seq 非法")
	}
	status := strings.TrimSpace(r.Status)
	if status == "" {
		status = PushPending
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO t_push_record (biz_no, push_seq, snapshot_hash, status, attempts, created_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT(biz_no, push_seq) DO NOTHING`,
		r.BizNo, r.PushSeq, nullStr(r.SnapshotHash), status, r.Attempts, fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 写入推送流水失败: %w", err)
	}
	return nil
}

// MarkPushResult 回填推送结果（SENT / FAILED）。
func (d *DB) MarkPushResult(ctx context.Context, bizNo string, pushSeq int64, status, lastErr string) error {
	var sent any
	if status == PushSent {
		sent = fmtTime(timeNow().UTC())
	}
	_, err := d.ExecContext(ctx, `
UPDATE t_push_record SET status = ?, last_error = ?, attempts = attempts + 1, sent_at = ?
WHERE biz_no = ? AND push_seq = ?`,
		status, nullStr(lastErr), sent, bizNo, pushSeq)
	if err != nil {
		return fmt.Errorf("store: 回填推送结果失败: %w", err)
	}
	return nil
}

// ListPushRecords 列出某业务单号的推送流水（按 push_seq 升序）。
func (d *DB) ListPushRecords(ctx context.Context, bizNo string) ([]PushRecord, error) {
	rows, err := d.QueryContext(ctx, pushRecordSelectSQL+` WHERE biz_no = ? ORDER BY push_seq`, bizNo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PushRecord
	for rows.Next() {
		r, err := scanPushRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CountPushRecords 统计某业务单号的推送条数（用例断言 / 自检）。
func (d *DB) CountPushRecords(ctx context.Context, bizNo string) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_push_record WHERE biz_no = ?`, bizNo).Scan(&n)
	return n, err
}

func scanPushRecord(s interface{ Scan(dest ...any) error }) (*PushRecord, error) {
	var (
		r       PushRecord
		created string
		sent    string
	)
	if err := s.Scan(&r.ID, &r.BizNo, &r.PushSeq, &r.SnapshotHash, &r.Status, &r.Attempts,
		&r.LastError, &created, &sent); err != nil {
		return nil, err
	}
	r.CreatedAt = parseTime(created)
	r.SentAt = parseTimePtr(sent)
	return &r, nil
}
