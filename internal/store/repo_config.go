package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ---------- 配置映射（approval_code / field_id / threshold / enum） ----------

// ListConfigMappings 按 map_kind 读取配置映射（不硬编码任何具体值，遵守纪律 8）。
func (d *DB) ListConfigMappings(ctx context.Context, kind string) ([]ConfigMappingRow, error) {
	rows, err := d.QueryContext(ctx, `
SELECT map_kind, map_key, map_value, COALESCE(doc_type,''), COALESCE(remark,'')
FROM t_config_mapping WHERE map_kind = ? ORDER BY map_key`, kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ConfigMappingRow
	for rows.Next() {
		var r ConfigMappingRow
		if err := rows.Scan(&r.MapKind, &r.MapKey, &r.MapValue, &r.DocType, &r.Remark); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertConfigMapping 写入/更新一条配置映射（独立连接）。
func (d *DB) UpsertConfigMapping(ctx context.Context, r ConfigMappingRow) error {
	return upsertConfigMapping(ctx, d, r)
}

// UpsertConfigMappingTx 事务内写入/更新一条配置映射。
func (d *DB) UpsertConfigMappingTx(ctx context.Context, tx *sql.Tx, r ConfigMappingRow) error {
	return upsertConfigMapping(ctx, tx, r)
}

// upsertConfigMapping 冲突目标含 `map_value`（migration 0006，B47 修复）：使 `ledger_type`
// 这类「doc_type → 台账类型」具备**一对多**语义（`PR` 同时落 `L02` 与 `L03`）。
// 其余映射类的唯一性由 `config.Validate()` 在应用层保证（导入是唯一写入口）。
//
// ★ 副作用必须记住：`map_value` 进唯一键后，**改 value 不再是覆盖而是新增一行**。
//
//	因此导入侧必须「先按映射类清空再写入」（见 `DeleteConfigMappingsKindTx`），
//	否则旧行残留会让同一 doc_type 同时写两个台账 —— 又一个静默缺陷。
func upsertConfigMapping(ctx context.Context, q execer, r ConfigMappingRow) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO t_config_mapping (map_kind, map_key, map_value, doc_type, remark, updated_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT(map_kind, map_key, COALESCE(doc_type,''), map_value) DO UPDATE SET
  remark = excluded.remark, updated_at = excluded.updated_at`,
		r.MapKind, r.MapKey, r.MapValue, nullStr(r.DocType), nullStr(r.Remark), fmtTime(timeNow().UTC()))
	return err
}

// DeleteConfigMappingsKindTx 清空某一映射类的全部行，返回删除条数。
//
// 供「导入 = 全量替换」使用：某映射类在配置文件中出现时，表内该类先清空再写入，
// 保证**表中内容恒等于文件内容**（无残留旧 value）。
func (d *DB) DeleteConfigMappingsKindTx(ctx context.Context, tx *sql.Tx, kind string) (int64, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM t_config_mapping WHERE map_kind = ?`, kind)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- 订阅状态 ----------

// GetSubscribeState 读取某 approval_code 的订阅状态。
func (d *DB) GetSubscribeState(ctx context.Context, code string) (*SubscribeState, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, approval_code, COALESCE(doc_type,''), subscribed, COALESCE(last_result,''),
       COALESCE(last_error,''), COALESCE(last_attempt_at,''), updated_at
FROM t_subscribe_state WHERE approval_code = ?`, code)
	return scanSubscribe(row)
}

// ListSubscribeStates 列出全部订阅状态。
func (d *DB) ListSubscribeStates(ctx context.Context) ([]SubscribeState, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id, approval_code, COALESCE(doc_type,''), subscribed, COALESCE(last_result,''),
       COALESCE(last_error,''), COALESCE(last_attempt_at,''), updated_at
FROM t_subscribe_state ORDER BY approval_code`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SubscribeState
	for rows.Next() {
		st, err := scanSubscribe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

// UpsertSubscribeState 写入/更新订阅状态（approval_code 唯一）。
func (d *DB) UpsertSubscribeState(ctx context.Context, st SubscribeState) error {
	subscribed := 0
	if st.Subscribed {
		subscribed = 1
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO t_subscribe_state
  (approval_code, doc_type, subscribed, last_result, last_error, last_attempt_at, updated_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(approval_code) DO UPDATE SET
  doc_type = COALESCE(NULLIF(excluded.doc_type,''), t_subscribe_state.doc_type),
  subscribed = excluded.subscribed, last_result = excluded.last_result,
  last_error = excluded.last_error, last_attempt_at = excluded.last_attempt_at,
  updated_at = excluded.updated_at`,
		st.ApprovalCode, nullStr(st.DocType), subscribed, nullStr(st.LastResult), nullStr(st.LastError),
		nullStr(fmtMaybeTime(st.LastAttemptAt)), fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 写入订阅状态失败: %w", err)
	}
	return nil
}

func scanSubscribe(s interface {
	Scan(dest ...any) error
}) (*SubscribeState, error) {
	var (
		st        SubscribeState
		sub       int
		attemptAt string
		updated   string
	)
	if err := s.Scan(&st.ID, &st.ApprovalCode, &st.DocType, &sub, &st.LastResult, &st.LastError, &attemptAt, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	st.Subscribed = sub == 1
	st.LastAttemptAt = parseTimePtr(attemptAt)
	st.UpdatedAt = parseTime(updated)
	return &st, nil
}

// ---------- 同步游标 ----------

// GetSyncCursor 读取对账游标；不存在返回 ErrNotFound。
func (d *DB) GetSyncCursor(ctx context.Context, code, kind string) (*SyncCursor, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, approval_code, cursor_kind, COALESCE(last_synced_at,''), COALESCE(last_run_at,''),
       last_missing, last_filled
FROM t_sync_cursor WHERE approval_code = ? AND cursor_kind = ?`, code, kind)
	var (
		c        SyncCursor
		syncedAt string
		runAt    string
	)
	if err := row.Scan(&c.ID, &c.ApprovalCode, &c.CursorKind, &syncedAt, &runAt, &c.LastMissing, &c.LastFilled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.LastSyncedAt = parseTimePtr(syncedAt)
	c.LastRunAt = parseTimePtr(runAt)
	return &c, nil
}

// UpsertSyncCursor 写入/更新对账游标。
func (d *DB) UpsertSyncCursor(ctx context.Context, c SyncCursor) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_sync_cursor (approval_code, cursor_kind, last_synced_at, last_run_at, last_missing, last_filled)
VALUES (?,?,?,?,?,?)
ON CONFLICT(approval_code, cursor_kind) DO UPDATE SET
  last_synced_at = COALESCE(excluded.last_synced_at, t_sync_cursor.last_synced_at),
  last_run_at = excluded.last_run_at, last_missing = excluded.last_missing, last_filled = excluded.last_filled`,
		c.ApprovalCode, c.CursorKind, nullStr(fmtMaybeTime(c.LastSyncedAt)), nullStr(fmtMaybeTime(c.LastRunAt)),
		c.LastMissing, c.LastFilled)
	return err
}

// fmtMaybeTime 将 *time.Time 格式化为字符串（nil 返回空串）。
func fmtMaybeTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return fmtTime(*t)
}
