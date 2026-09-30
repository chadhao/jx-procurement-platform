package store

// 角色代理人 t_role_agent（N-028）——「只停用不删 + 每角色至多 1 条 active」。
// ★ 代理人**不参与审批人解析**（用户「不需要替补」）：本表当前唯一运行时消费端是
//   负向守卫（chain 解析忽略本表）；正向（转交/回退按代理人）属 M9。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RoleAgent 一条代理人配置。
type RoleAgent struct {
	ID          int64
	RoleKey     string
	AgentOpenID string
	State       string // active | retired
	Note        string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedBy   string
	UpdatedAt   time.Time
}

// ErrRoleAgentConflict 同角色已有生效代理人（部分唯一索引）或同键重复。
var ErrRoleAgentConflict = errors.New("store: 同角色已存在生效代理人")

// ErrRoleAgentNotFound 记录不存在。
var ErrRoleAgentNotFound = errors.New("store: 代理人记录不存在")

// ListRoleAgents 列出（roleKey/state 为空则不过滤）；active 在前。
func (d *DB) ListRoleAgents(ctx context.Context, roleKey, state string) ([]RoleAgent, error) {
	q := `SELECT id, role_key, agent_open_id, state, note, created_by, created_at, updated_by, updated_at
FROM t_role_agent`
	var args []any
	var conds []string
	if roleKey != "" {
		conds = append(conds, "role_key = ?")
		args = append(args, roleKey)
	}
	if state != "" {
		conds = append(conds, "state = ?")
		args = append(args, state)
	}
	for i, c := range conds {
		if i == 0 {
			q += " WHERE " + c
		} else {
			q += " AND " + c
		}
	}
	q += ` ORDER BY CASE state WHEN 'active' THEN 0 ELSE 1 END, role_key, id`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 列出代理人失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []RoleAgent{}
	for rows.Next() {
		var r RoleAgent
		var created, updated string
		if err := rows.Scan(&r.ID, &r.RoleKey, &r.AgentOpenID, &r.State, &r.Note,
			&r.CreatedBy, &created, &r.UpdatedBy, &updated); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = parseTimeLoose(created)
		r.UpdatedAt, _ = parseTimeLoose(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRoleAgent 按 id 取。
func (d *DB) GetRoleAgent(ctx context.Context, id int64) (*RoleAgent, error) {
	r := &RoleAgent{}
	var created, updated string
	err := d.QueryRowContext(ctx, `
SELECT id, role_key, agent_open_id, state, note, created_by, created_at, updated_by, updated_at
FROM t_role_agent WHERE id = ?`, id).Scan(&r.ID, &r.RoleKey, &r.AgentOpenID, &r.State, &r.Note,
		&r.CreatedBy, &created, &r.UpdatedBy, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRoleAgentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询代理人失败: %w", err)
	}
	r.CreatedAt, _ = parseTimeLoose(created)
	r.UpdatedAt, _ = parseTimeLoose(updated)
	return r, nil
}

// FindActiveRoleAgent 查某角色的**生效**代理人（无则 (nil, nil)）。
func (d *DB) FindActiveRoleAgent(ctx context.Context, roleKey string) (*RoleAgent, error) {
	list, err := d.ListRoleAgents(ctx, roleKey, "active")
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// InsertRoleAgent 新增（同角色已有 active ⇒ ErrRoleAgentConflict，含部分唯一索引兜底）。
func (d *DB) InsertRoleAgent(ctx context.Context, roleKey, agentOpenID, note, createdBy string) (*RoleAgent, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := d.ExecContext(ctx, `
INSERT INTO t_role_agent (role_key, agent_open_id, state, note, created_by, created_at, updated_by, updated_at)
VALUES (?,?, 'active', ?, ?, ?, ?, ?)`,
		roleKey, agentOpenID, note, createdBy, now, createdBy, now)
	if err != nil {
		if isUniqueErr(err) {
			return nil, ErrRoleAgentConflict
		}
		return nil, fmt.Errorf("store: 新增代理人失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetRoleAgent(ctx, id)
}

// UpdateRoleAgent 改人 / 备注 / 状态（空值不改）。
func (d *DB) UpdateRoleAgent(ctx context.Context, id int64, agentOpenID, note, state, updatedBy string) (*RoleAgent, error) {
	cur, err := d.GetRoleAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	nextAgent := cur.AgentOpenID
	if agentOpenID != "" {
		nextAgent = agentOpenID
	}
	nextNote := cur.Note
	if note != "" {
		nextNote = note
	}
	nextState := cur.State
	if state != "" {
		nextState = state
	}
	_, err = d.ExecContext(ctx, `
UPDATE t_role_agent SET agent_open_id = ?, note = ?, state = ?, updated_by = ?, updated_at = ?
WHERE id = ?`,
		nextAgent, nextNote, nextState, updatedBy,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		if isUniqueErr(err) {
			return nil, ErrRoleAgentConflict
		}
		return nil, fmt.Errorf("store: 更新代理人失败: %w", err)
	}
	return d.GetRoleAgent(ctx, id)
}
