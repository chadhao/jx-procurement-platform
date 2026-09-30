package store

// 运营性常量表 t_constant（T2 / R-24）——「只停用不删 + 值快照」纪律的存储层。
//
// ★ 无物理删除路径：API 层只提供 status=retired（guardrail 在 httpapi）；
// ★ 每次变更由 API 层写 t_audit_log（操作人/表/条目前后值）；
// ★ 种子播种（spec/constants.json#tables[].seed）＝INSERT OR IGNORE ——
//   **只是可用起点，不是权威清单**（管理员可自由增删改，不必走裁定）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ConstantRow 一条常量。
type ConstantRow struct {
	ID        int64
	TableKey  string
	Value     string
	SortOrder int
	Status    string // active | retired
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrConstantNotFound 常量不存在（或按过滤条件无匹配）。
var ErrConstantNotFound = errors.New("store: 常量不存在")

// ErrConstantConflict 同表同值已存在（唯一约束）。
var ErrConstantConflict = errors.New("store: 同表同值已存在")

// ListConstants 列出某表的常量（status 空 = 全部；active 在前、按 sort_order/id）。
func (d *DB) ListConstants(ctx context.Context, tableKey, status string) ([]ConstantRow, error) {
	query := `SELECT id, table_key, value, sort_order, status, created_at, updated_at
FROM t_constant WHERE table_key = ?`
	args := []any{tableKey}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY CASE status WHEN 'active' THEN 0 ELSE 1 END, sort_order, id`
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 列出常量失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ConstantRow{}
	for rows.Next() {
		var r ConstantRow
		var created, updated string
		if err := rows.Scan(&r.ID, &r.TableKey, &r.Value, &r.SortOrder, &r.Status, &created, &updated); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = parseTimeLoose(created)
		r.UpdatedAt, _ = parseTimeLoose(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetConstant 按 id 取。
func (d *DB) GetConstant(ctx context.Context, id int64) (*ConstantRow, error) {
	r := &ConstantRow{}
	var created, updated string
	err := d.QueryRowContext(ctx, `
SELECT id, table_key, value, sort_order, status, created_at, updated_at
FROM t_constant WHERE id = ?`, id).Scan(&r.ID, &r.TableKey, &r.Value, &r.SortOrder, &r.Status, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConstantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询常量失败: %w", err)
	}
	r.CreatedAt, _ = parseTimeLoose(created)
	r.UpdatedAt, _ = parseTimeLoose(updated)
	return r, nil
}

// ListActiveConstantValues 某表的 active 值集合（提交校验与 meta 下发用）。
func (d *DB) ListActiveConstantValues(ctx context.Context, tableKey string) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
SELECT value FROM t_constant WHERE table_key = ? AND status = 'active' ORDER BY sort_order, id`, tableKey)
	if err != nil {
		return nil, fmt.Errorf("store: 查询常量 active 值失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// InsertConstant 新增（同表同值 ⇒ ErrConstantConflict，调用方映射 40900）。
func (d *DB) InsertConstant(ctx context.Context, tableKey, value string, sortOrder int) (*ConstantRow, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := d.ExecContext(ctx, `
INSERT INTO t_constant (table_key, value, sort_order, status, created_at, updated_at)
VALUES (?,?,?,'active',?,?)`, tableKey, value, sortOrder, now, now)
	if err != nil {
		if isUniqueErr(err) {
			return nil, ErrConstantConflict
		}
		return nil, fmt.Errorf("store: 新增常量失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetConstant(ctx, id)
}

// UpdateConstant 改显示值 / 排序 / 状态（**唯一更新入口 —— 无 Delete**）。
// status ∈ {active, retired}；value 空则不改。
func (d *DB) UpdateConstant(ctx context.Context, id int64, value string, sortOrder *int, status string) (*ConstantRow, error) {
	cur, err := d.GetConstant(ctx, id)
	if err != nil {
		return nil, err
	}
	nextValue := cur.Value
	if value != "" {
		nextValue = value
	}
	nextSort := cur.SortOrder
	if sortOrder != nil {
		nextSort = *sortOrder
	}
	nextStatus := cur.Status
	if status != "" {
		nextStatus = status
	}
	_, err = d.ExecContext(ctx, `
UPDATE t_constant SET value = ?, sort_order = ?, status = ?, updated_at = ?
WHERE id = ?`, nextValue, nextSort, nextStatus, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		if isUniqueErr(err) {
			return nil, ErrConstantConflict
		}
		return nil, fmt.Errorf("store: 更新常量失败: %w", err)
	}
	return d.GetConstant(ctx, id)
}

// SeedConstants 从 spec 种子播种（幂等：INSERT OR IGNORE；**只是可用起点**）。
func (d *DB) SeedConstants(ctx context.Context, tableKey string, values []string) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	inserted := 0
	for i, v := range values {
		res, err := d.ExecContext(ctx, `
INSERT INTO t_constant (table_key, value, sort_order, status, created_at, updated_at)
VALUES (?,?,?,'active',?,?) ON CONFLICT(table_key, value) DO NOTHING`,
			tableKey, v, i, now, now)
		if err != nil {
			return inserted, fmt.Errorf("store: 播种常量 %q/%q 失败: %w", tableKey, v, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

// parseTimeLoose 兼容 RFC3339Nano 与空串。
func parseTimeLoose(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
