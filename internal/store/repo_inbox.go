package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ---------- 事件收件箱（幂等） ----------

// InsertInboxIgnore 幂等写入收件箱：idem_key 冲突时忽略。
// 返回该 idem_key 对应的行 id，以及本次是否真的新增（false = 幂等命中）。
func (d *DB) InsertInboxIgnore(ctx context.Context, q execer, row *InboxRow) (int64, bool, error) {
	if q == nil {
		q = d
	}
	res, err := q.ExecContext(ctx, `
INSERT OR IGNORE INTO t_event_inbox
  (idem_key, event_type, instance_code, status, event_id, payload, process_state, retry_count, received_at)
VALUES (?,?,?,?,?,?,?,0,?)`,
		row.IdemKey, row.EventType, row.InstanceCode, nullStr(row.Status), nullStr(row.EventID),
		row.Payload, defaultStr(row.ProcessState, "PENDING"), fmtTime(row.ReceivedAt),
	)
	if err != nil {
		return 0, false, fmt.Errorf("store: 写入收件箱失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	inserted := n > 0

	var id int64
	if err := q.QueryRowContext(ctx, `SELECT id FROM t_event_inbox WHERE idem_key = ?`, row.IdemKey).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("store: 回读收件箱 id 失败: %w", err)
	}
	return id, inserted, nil
}

const inboxSelectSQL = `
SELECT id, idem_key, event_type, COALESCE(instance_code,''), COALESCE(status,''), COALESCE(event_id,''),
       payload, process_state, retry_count, COALESCE(last_error,''), received_at,
       COALESCE(processed_at,'')
FROM t_event_inbox`

// GetInbox 按 id 读取收件箱行。
func (d *DB) GetInbox(ctx context.Context, id int64) (*InboxRow, error) {
	row := d.QueryRowContext(ctx, inboxSelectSQL+` WHERE id = ?`, id)
	return scanInbox(row)
}

// ListInboxByState 按处理状态读取收件箱行（用于运维排查）。
func (d *DB) ListInboxByState(ctx context.Context, state string, limit int) ([]InboxRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, inboxSelectSQL+` WHERE process_state = ? ORDER BY id LIMIT ?`, state, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []InboxRow
	for rows.Next() {
		ib, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ib)
	}
	return out, rows.Err()
}

// MarkInbox 更新收件箱处理状态（含重试次数、错误与处理时刻）。
func (d *DB) MarkInbox(ctx context.Context, q execer, id int64, state string, retryCount int, lastError string, processedAt *time.Time) error {
	if q == nil {
		q = d
	}
	var processed any
	if processedAt != nil {
		processed = fmtTime(*processedAt)
	}
	_, err := q.ExecContext(ctx, `
UPDATE t_event_inbox SET process_state = ?, retry_count = ?, last_error = ?, processed_at = ? WHERE id = ?`,
		state, retryCount, nullStr(lastError), processed, id)
	if err != nil {
		return fmt.Errorf("store: 更新收件箱状态失败: %w", err)
	}
	return nil
}

// scanInbox 扫描收件箱行。
func scanInbox(s interface {
	Scan(dest ...any) error
}) (*InboxRow, error) {
	var (
		ib        InboxRow
		received  string
		processed string
	)
	if err := s.Scan(&ib.ID, &ib.IdemKey, &ib.EventType, &ib.InstanceCode, &ib.Status, &ib.EventID,
		&ib.Payload, &ib.ProcessState, &ib.RetryCount, &ib.LastError, &received, &processed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ib.ReceivedAt = parseTime(received)
	ib.ProcessedAt = parseTimePtr(processed)
	return &ib, nil
}

// ---------- 异步作业 ----------

// EnqueueJobIgnore 幂等建作业：(inbox_id, job_type) 冲突时忽略。
func (d *DB) EnqueueJobIgnore(ctx context.Context, q execer, inboxID int64, jobType string, now time.Time) (bool, error) {
	if q == nil {
		q = d
	}
	res, err := q.ExecContext(ctx, `
INSERT OR IGNORE INTO t_worker_job (inbox_id, job_type, state, attempts, created_at, updated_at)
VALUES (?,?, 'QUEUED', 0, ?, ?)`, inboxID, jobType, fmtTime(now), fmtTime(now))
	if err != nil {
		return false, fmt.Errorf("store: 建异步作业失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// DueJobs 取出到期的 QUEUED 作业（按入队顺序）。
func (d *DB) DueJobs(ctx context.Context, now time.Time, limit int) ([]WorkerJob, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.QueryContext(ctx, `
SELECT id, inbox_id, job_type, state, attempts, COALESCE(next_run_at,''), COALESCE(last_error,''), created_at, updated_at
FROM t_worker_job
WHERE state = 'QUEUED' AND (next_run_at IS NULL OR next_run_at <= ?)
ORDER BY id LIMIT ?`, fmtTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []WorkerJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// MarkJob 更新作业状态、尝试次数、下次运行时刻与错误信息。
func (d *DB) MarkJob(ctx context.Context, q execer, jobID int64, state string, attempts int, nextRunAt *time.Time, lastError string) error {
	if q == nil {
		q = d
	}
	var next any
	if nextRunAt != nil {
		next = fmtTime(*nextRunAt)
	}
	_, err := q.ExecContext(ctx, `
UPDATE t_worker_job SET state = ?, attempts = ?, next_run_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		state, attempts, next, nullStr(lastError), fmtTime(timeNow().UTC()), jobID)
	if err != nil {
		return fmt.Errorf("store: 更新作业状态失败: %w", err)
	}
	return nil
}

// CountJobsByState 统计某状态作业数（worker 队列积压指标）。
func (d *DB) CountJobsByState(ctx context.Context, state string) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_worker_job WHERE state = ?`, state).Scan(&n)
	return n, err
}

func scanJob(s interface {
	Scan(dest ...any) error
}) (*WorkerJob, error) {
	var (
		j       WorkerJob
		nextRun string
		created string
		updated string
	)
	if err := s.Scan(&j.ID, &j.InboxID, &j.JobType, &j.State, &j.Attempts, &nextRun, &j.LastError, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	j.NextRunAt = parseTimePtr(nextRun)
	j.CreatedAt = parseTime(created)
	j.UpdatedAt = parseTime(updated)
	return &j, nil
}

// ---------- 死信 ----------

// InsertDeadletter 落一条死信（不静默丢失，FR-M3-07 / TC-30）。
func (d *DB) InsertDeadletter(ctx context.Context, q execer, inboxID int64, reason, payload string) (int64, error) {
	if q == nil {
		q = d
	}
	res, err := q.ExecContext(ctx, `
INSERT INTO t_deadletter (inbox_id, reason, payload, replay_count, created_at) VALUES (?,?,?,0,?)`,
		inboxID, reason, nullStr(payload), fmtTime(timeNow().UTC()))
	if err != nil {
		return 0, fmt.Errorf("store: 写入死信失败: %w", err)
	}
	return res.LastInsertId()
}

// GetDeadletter 按 id 读取死信。
func (d *DB) GetDeadletter(ctx context.Context, id int64) (*Deadletter, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, inbox_id, reason, COALESCE(payload,''), replay_count, created_at, COALESCE(last_replayed_at,'')
FROM t_deadletter WHERE id = ?`, id)
	var (
		dl       Deadletter
		created  string
		replayed string
	)
	if err := row.Scan(&dl.ID, &dl.InboxID, &dl.Reason, &dl.Payload, &dl.ReplayCount, &created, &replayed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	dl.CreatedAt = parseTime(created)
	dl.LastReplayedAt = parseTimePtr(replayed)
	return &dl, nil
}

// ListDeadletters 列出死信（供运维查看与重放）。
func (d *DB) ListDeadletters(ctx context.Context, limit int) ([]Deadletter, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `
SELECT id, inbox_id, reason, COALESCE(payload,''), replay_count, created_at, COALESCE(last_replayed_at,'')
FROM t_deadletter ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Deadletter
	for rows.Next() {
		var (
			dl       Deadletter
			created  string
			replayed string
		)
		if err := rows.Scan(&dl.ID, &dl.InboxID, &dl.Reason, &dl.Payload, &dl.ReplayCount, &created, &replayed); err != nil {
			return nil, err
		}
		dl.CreatedAt = parseTime(created)
		dl.LastReplayedAt = parseTimePtr(replayed)
		out = append(out, dl)
	}
	return out, rows.Err()
}

// CountDeadletters 统计死信总数（/readyz 指标）。
func (d *DB) CountDeadletters(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_deadletter`).Scan(&n)
	return n, err
}

// MarkReplayed 记录死信被人工重放（replay_count++）。
func (d *DB) MarkReplayed(ctx context.Context, q execer, id int64, at time.Time) error {
	if q == nil {
		q = d
	}
	_, err := q.ExecContext(ctx, `
UPDATE t_deadletter SET replay_count = replay_count + 1, last_replayed_at = ? WHERE id = ?`, fmtTime(at), id)
	return err
}

// ResetJobForReplay 将某 inbox 的作业重置为可再次调度（人工重放用）。
func (d *DB) ResetJobForReplay(ctx context.Context, q execer, inboxID int64) error {
	if q == nil {
		q = d
	}
	_, err := q.ExecContext(ctx, `
UPDATE t_worker_job SET state = 'QUEUED', attempts = 0, next_run_at = NULL, last_error = NULL, updated_at = ?
WHERE inbox_id = ?`, fmtTime(timeNow().UTC()), inboxID)
	return err
}
