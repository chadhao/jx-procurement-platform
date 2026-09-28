package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// repo_org.go —— 飞书通讯录镜像读写层（migration 0012；docs/08 §4.2）。
//
// ★★ 边界（docs/08 §4.10「镜像 ≠ 权限」）：
//   - 本文件只读/写 t_org_* 镜像表，**不触碰** t_user_role 的写路径
//     （目录查询对角色的 LEFT JOIN 是只读借用，不构成写通道）；
//   - 软删语义：只置 is_deleted=1，永不物理删除（口径④）；
//   - 历史解析查询（MapOrgUsersByOpenIDs）**不过滤 is_deleted**（docs/08 C-E），
//     目录/选择器查询（ListOrgDepartmentNames / ListOrgUserDirectory）才过滤。

// ---------- 部门镜像 ----------

// UpsertOrgDepartment 写入/更新部门镜像（幂等：PK 冲突即更新；不复活软删行——
// 复活由 ApplyFull 的显式 is_deleted 计算完成，见参数 isDeleted）。
func (d *DB) UpsertOrgDepartment(ctx context.Context, g *OrgDepartment) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_org_department
  (open_department_id, department_id, parent_open_department_id, name, name_path,
   is_deleted, raw_json, first_seen_at, last_seen_at, updated_at, source)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(open_department_id) DO UPDATE SET
  department_id = excluded.department_id,
  parent_open_department_id = excluded.parent_open_department_id,
  name = excluded.name,
  name_path = excluded.name_path,
  is_deleted = excluded.is_deleted,
  raw_json = excluded.raw_json,
  last_seen_at = excluded.last_seen_at,
  updated_at = excluded.updated_at,
  source = excluded.source`,
		g.OpenDepartmentID, g.DepartmentID, g.ParentOpenDepartmentID,
		g.Name, g.NamePath, boolToInt(g.IsDeleted), defaultStr(g.RawJSON, "{}"),
		fmtTime(g.FirstSeenAt), fmtTime(g.LastSeenAt), fmtTime(g.UpdatedAt), g.Source)
	if err != nil {
		return fmt.Errorf("store: 写入部门镜像失败: %w", err)
	}
	return nil
}

// ListOrgDepartments 全量读出部门镜像（含软删行；供对账 diff 用）。
func (d *DB) ListOrgDepartments(ctx context.Context) ([]OrgDepartment, error) {
	rows, err := d.QueryContext(ctx, `
SELECT open_department_id, department_id, parent_open_department_id, name, name_path,
       is_deleted, raw_json, first_seen_at, last_seen_at, updated_at, source
FROM t_org_department`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OrgDepartment
	for rows.Next() {
		g, scanErr := scanOrgDepartment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func scanOrgDepartment(s interface{ Scan(dest ...any) error }) (OrgDepartment, error) {
	var (
		g               OrgDepartment
		deleted         int
		first, last, up string
	)
	if err := s.Scan(&g.OpenDepartmentID, &g.DepartmentID, &g.ParentOpenDepartmentID,
		&g.Name, &g.NamePath, &deleted, &g.RawJSON, &first, &last, &up, &g.Source); err != nil {
		return g, err
	}
	g.IsDeleted = deleted == 1
	g.FirstSeenAt = parseTime(first)
	g.LastSeenAt = parseTime(last)
	g.UpdatedAt = parseTime(up)
	return g, nil
}

// SoftDeleteOrgDepartmentsExcept 把「本地有、远端无」的部门置软删（is_deleted=1）。
//
// ★ 只软删「仍在用」的行（is_deleted=0 才计数）；永不物理删除。ids 为本次全量
// 观测到的 open_department_id 集合（远端为准）。返回本次新软删的行数。
func (d *DB) SoftDeleteOrgDepartmentsExcept(ctx context.Context, seen map[string]bool, now time.Time) (int64, error) {
	rows, err := d.QueryContext(ctx, `SELECT open_department_id FROM t_org_department WHERE is_deleted = 0`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return d.softDeleteByIDs(ctx, "t_org_department", "open_department_id", stale, now)
}

// softDeleteByIDs 分批把指定 ID 的在用行置软删（SQLite 变量上限防护：每批 500）。
func (d *DB) softDeleteByIDs(ctx context.Context, table, idCol string, ids []string, now time.Time) (int64, error) {
	var total int64
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := start + batch
		if end > len(ids) {
			end = len(ids)
		}
		part := ids[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?,", len(part)), ",")
		// ★ 参数顺序必须与 SQL 中占位符顺序一致：SET updated_at = ? 在前、IN (...) 在后。
		args := make([]any, 0, len(part)+1)
		args = append(args, fmtTime(now))
		for _, id := range part {
			args = append(args, id)
		}
		res, err := d.ExecContext(ctx,
			`UPDATE `+table+` SET is_deleted = 1, updated_at = ? WHERE `+idCol+` IN (`+placeholders+`) AND is_deleted = 0`,
			args...)
		if err != nil {
			return total, fmt.Errorf("store: 软删 %s 失败: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// ---------- 人员镜像 ----------

// UpsertOrgUser 写入/更新人员镜像（幂等；IsDeleted 由调用方按「远端状态」显式给值，
// 使「离职/删除 → 软删、复活 → 复用」在同一条 UPSERT 内收敛）。
func (d *DB) UpsertOrgUser(ctx context.Context, u *OrgUser) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_org_user
  (open_id, union_id, user_id, name, employee_status,
   is_resigned, is_exited, is_frozen, is_activated, is_unjoin,
   primary_department_id, department_ids, is_deleted, raw_json,
   first_seen_at, last_seen_at, updated_at, source)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(open_id) DO UPDATE SET
  union_id = excluded.union_id,
  user_id = excluded.user_id,
  name = excluded.name,
  employee_status = excluded.employee_status,
  is_resigned = excluded.is_resigned,
  is_exited = excluded.is_exited,
  is_frozen = excluded.is_frozen,
  is_activated = excluded.is_activated,
  is_unjoin = excluded.is_unjoin,
  primary_department_id = excluded.primary_department_id,
  department_ids = excluded.department_ids,
  is_deleted = excluded.is_deleted,
  raw_json = excluded.raw_json,
  last_seen_at = excluded.last_seen_at,
  updated_at = excluded.updated_at,
  source = excluded.source`,
		u.OpenID, u.UnionID, u.UserID, u.Name, u.EmployeeStatus,
		boolToInt(u.IsResigned), boolToInt(u.IsExited), boolToInt(u.IsFrozen),
		boolToInt(u.IsActivated), boolToInt(u.IsUnjoin),
		u.PrimaryDepartmentID, defaultStr(marshalStrings(u.DepartmentIDs), "[]"),
		boolToInt(u.IsDeleted), defaultStr(u.RawJSON, "{}"),
		fmtTime(u.FirstSeenAt), fmtTime(u.LastSeenAt), fmtTime(u.UpdatedAt), u.Source)
	if err != nil {
		return fmt.Errorf("store: 写入人员镜像失败: %w", err)
	}
	return nil
}

// ListOrgUsers 全量读出人员镜像（含软删行；供对账 diff 用）。
func (d *DB) ListOrgUsers(ctx context.Context) ([]OrgUser, error) {
	rows, err := d.QueryContext(ctx, `
SELECT open_id, union_id, user_id, name, employee_status,
       is_resigned, is_exited, is_frozen, is_activated, is_unjoin,
       primary_department_id, department_ids, is_deleted, raw_json,
       first_seen_at, last_seen_at, updated_at, source
FROM t_org_user`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OrgUser
	for rows.Next() {
		u, scanErr := scanOrgUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func scanOrgUser(s interface{ Scan(dest ...any) error }) (OrgUser, error) {
	var (
		u                                                    OrgUser
		resigned, exited, frozen, activated, unjoin, deleted int
		first, last, up                                      string
		depts                                                string
	)
	if err := s.Scan(&u.OpenID, &u.UnionID, &u.UserID, &u.Name, &u.EmployeeStatus,
		&resigned, &exited, &frozen, &activated, &unjoin,
		&u.PrimaryDepartmentID, &depts, &deleted, &u.RawJSON,
		&first, &last, &up, &u.Source); err != nil {
		return u, err
	}
	u.IsResigned, u.IsExited, u.IsFrozen = resigned == 1, exited == 1, frozen == 1
	u.IsActivated, u.IsUnjoin, u.IsDeleted = activated == 1, unjoin == 1, deleted == 1
	u.DepartmentIDs = unmarshalStrings(depts)
	u.FirstSeenAt, u.LastSeenAt, u.UpdatedAt = parseTime(first), parseTime(last), parseTime(up)
	return u, nil
}

// SoftDeleteOrgUsersExcept 把「本地有、远端无」的人员置软删（同部门版语义）。
func (d *DB) SoftDeleteOrgUsersExcept(ctx context.Context, seen map[string]bool, now time.Time) (int64, error) {
	rows, err := d.QueryContext(ctx, `SELECT open_id FROM t_org_user WHERE is_deleted = 0`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return d.softDeleteByIDs(ctx, "t_org_user", "open_id", stale, now)
}

// GetOrgDepartment 按权威主键读取单个部门镜像（含软删行；事件增量回填 first_seen_at 用）。
// 无行返回 ErrNotFound。
func (d *DB) GetOrgDepartment(ctx context.Context, openDepartmentID string) (*OrgDepartment, error) {
	row := d.QueryRowContext(ctx, `
SELECT open_department_id, department_id, parent_open_department_id, name, name_path,
       is_deleted, raw_json, first_seen_at, last_seen_at, updated_at, source
FROM t_org_department WHERE open_department_id = ?`, openDepartmentID)
	g, err := scanOrgDepartment(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &g, nil
}

// SoftDeleteOrgDepartment 把单个部门置软删（事件 contact.department.deleted_v3 路径）。
//
// ★ 口径（docs/08 §4.8 批次二）：**只软删部门本身**，不级联——其下人员归属由后续
// user 事件 / 定期对账修正，避免级联误删。行已软删 ⇒ 幂等返回 (false, nil)。
// 永不物理删除。返回本次是否新软删。
func (d *DB) SoftDeleteOrgDepartment(ctx context.Context, openDepartmentID string, now time.Time) (bool, error) {
	res, err := d.ExecContext(ctx,
		`UPDATE t_org_department SET is_deleted = 1, updated_at = ? WHERE open_department_id = ? AND is_deleted = 0`,
		fmtTime(now), openDepartmentID)
	if err != nil {
		return false, fmt.Errorf("store: 软删部门 %s 失败: %w", openDepartmentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SoftDeleteOrgUser 把单个人员置软删（事件 contact.user.deleted_v3 路径；语义同部门版）。
func (d *DB) SoftDeleteOrgUser(ctx context.Context, openID string, now time.Time) (bool, error) {
	res, err := d.ExecContext(ctx,
		`UPDATE t_org_user SET is_deleted = 1, updated_at = ? WHERE open_id = ? AND is_deleted = 0`,
		fmtTime(now), openID)
	if err != nil {
		return false, fmt.Errorf("store: 软删人员 %s 失败: %w", openID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetOrgUser 按 open_id 读取单个人员镜像（含软删行）。无行返回 ErrNotFound。
func (d *DB) GetOrgUser(ctx context.Context, openID string) (*OrgUser, error) {
	row := d.QueryRowContext(ctx, `
SELECT open_id, union_id, user_id, name, employee_status,
       is_resigned, is_exited, is_frozen, is_activated, is_unjoin,
       primary_department_id, department_ids, is_deleted, raw_json,
       first_seen_at, last_seen_at, updated_at, source
FROM t_org_user WHERE open_id = ?`, openID)
	u, err := scanOrgUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// GetLatestOrgSyncRun 读取最近一条运行流水（供 /healthz 的 org_sync 段展示
// 「最近对账结果」）。无行返回 ErrNotFound。
func (d *DB) GetLatestOrgSyncRun(ctx context.Context) (*OrgSyncRun, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, run_at, trigger, result, dept_added, dept_updated, dept_soft_deleted,
       user_added, user_updated, user_soft_deleted, field_gaps_json, error, duration_ms
FROM t_org_sync_run ORDER BY id DESC LIMIT 1`)
	var (
		r                     OrgSyncRun
		attempt, gaps, errStr string
	)
	if err := row.Scan(&r.ID, &attempt, &r.Trigger, &r.Result,
		&r.DeptAdded, &r.DeptUpdated, &r.DeptSoftDeleted,
		&r.UserAdded, &r.UserUpdated, &r.UserSoftDeleted,
		&gaps, &errStr, &r.DurationMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.RunAt = parseTime(attempt)
	r.FieldGapsJSON = gaps
	r.Error = errStr
	return &r, nil
}

// TouchOrgSyncEventAt 只推进 last_event_at（事件增量处理成功的时刻）。
//
// ★ 为什么不用 Get+Save 组合：事件 worker 池 4 并发，与全量同步可能交错，
// Get+Save 会整行覆盖互相踩字段；本方法单语句只动两列（INSERT 兜底首行，
// ON CONFLICT 只更新 last_event_at/updated_at，其余列保持既有值不动）。
func (d *DB) TouchOrgSyncEventAt(ctx context.Context, now time.Time) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_org_sync_state (id, last_event_at, updated_at) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  last_event_at = excluded.last_event_at,
  updated_at = excluded.updated_at`,
		fmtTime(now), fmtTime(now))
	if err != nil {
		return fmt.Errorf("store: 推进 last_event_at 失败: %w", err)
	}
	return nil
}

// ---------- 目录查询（/api/org/* 数据源；过滤 is_deleted=0） ----------

// ListOrgDepartmentNames 返回镜像中**在用**部门的名称清单（非空、去重、字典序）。
//
// ★ 空镜像 ⇒ 返回空切片（不报错）——与既有 /api/org/departments 空表语义一致。
func (d *DB) ListOrgDepartmentNames(ctx context.Context) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
SELECT DISTINCT name FROM t_org_department
WHERE is_deleted = 0 AND TRIM(name) <> ''
ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(name))
	}
	return out, rows.Err()
}

// ListOrgUserDirectory 返回镜像中**在用**人员的目录视图（全员覆盖，含尚未配角色者）。
//
// ★ 镜像 ≠ 权限：role 来自 LEFT JOIN t_user_role（只读借用；未配角色者＝空串）；
// department＝主部门名称（镜像自解析；未配角色者同样有值——这正是数据源切换的意义）。
func (d *DB) ListOrgUserDirectory(ctx context.Context) ([]OrgUserView, error) {
	rows, err := d.QueryContext(ctx, `
SELECT u.open_id, COALESCE(u.name,''), COALESCE(r.role,''), COALESCE(g.name,'')
FROM t_org_user u
LEFT JOIN t_org_department g ON g.open_department_id = u.primary_department_id
LEFT JOIN t_user_role r      ON r.open_id = u.open_id
WHERE u.is_deleted = 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []OrgUserView{}
	for rows.Next() {
		var v OrgUserView
		if err := rows.Scan(&v.OpenID, &v.Name, &v.Role, &v.Department); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MapOrgUsersByOpenIDs 批量按 open_id 取人员展示视图（姓名 + 主部门名）。
//
// ★★ 历史解析口径（docs/08 C-E）：**不过滤 is_deleted** —— 软删/离职人员仍须支撑
// 历史单据（任务办理人）的姓名显示。查不到的 open_id 不进返回 map（调用方留空）。
func (d *DB) MapOrgUsersByOpenIDs(ctx context.Context, openIDs []string) (map[string]OrgUserView, error) {
	out := make(map[string]OrgUserView, len(openIDs))
	uniq := make([]string, 0, len(openIDs))
	seen := make(map[string]bool, len(openIDs))
	for _, id := range openIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(uniq)), ",")
	args := make([]any, len(uniq))
	for i, id := range uniq {
		args[i] = id
	}
	rows, err := d.QueryContext(ctx, `
SELECT u.open_id, COALESCE(u.name,''), COALESCE(g.name,'')
FROM t_org_user u
LEFT JOIN t_org_department g ON g.open_department_id = u.primary_department_id
WHERE u.open_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v OrgUserView
		if err := rows.Scan(&v.OpenID, &v.Name, &v.Department); err != nil {
			return nil, err
		}
		out[v.OpenID] = v
	}
	return out, rows.Err()
}

// ---------- 同步状态（单行）与运行流水 ----------

// GetOrgSyncState 读取同步状态；无行返回 ErrNotFound（首次运行前的合法态）。
func (d *DB) GetOrgSyncState(ctx context.Context) (*OrgSyncState, error) {
	row := d.QueryRowContext(ctx, `
SELECT last_full_attempt_at, last_full_success_at, last_full_error,
       last_full_dept_count, last_full_user_count, last_event_at, updated_at
FROM t_org_sync_state WHERE id = 1`)
	var (
		st                            OrgSyncState
		attempt, success, ev, updated string
	)
	if err := row.Scan(&attempt, &success, &st.LastFullError,
		&st.LastFullDeptCount, &st.LastFullUserCount, &ev, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	st.LastFullAttemptAt = parseTime(attempt)
	st.LastFullSuccessAt = parseTime(success)
	st.LastEventAt = parseTime(ev)
	st.UpdatedAt = parseTime(updated)
	return &st, nil
}

// SaveOrgSyncState 写入同步状态（单行 UPSERT；仅成功时推进 last_full_success_at，
// 由调用方保证——本方法只做忠实写入）。
func (d *DB) SaveOrgSyncState(ctx context.Context, st *OrgSyncState) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_org_sync_state
  (id, last_full_attempt_at, last_full_success_at, last_full_error,
   last_full_dept_count, last_full_user_count, last_event_at, updated_at)
VALUES (1,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  last_full_attempt_at = excluded.last_full_attempt_at,
  last_full_success_at = excluded.last_full_success_at,
  last_full_error = excluded.last_full_error,
  last_full_dept_count = excluded.last_full_dept_count,
  last_full_user_count = excluded.last_full_user_count,
  last_event_at = excluded.last_event_at,
  updated_at = excluded.updated_at`,
		fmtTime(st.LastFullAttemptAt), fmtTime(st.LastFullSuccessAt), st.LastFullError,
		st.LastFullDeptCount, st.LastFullUserCount, fmtTime(st.LastEventAt),
		fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 写入同步状态失败: %w", err)
	}
	return nil
}

// InsertOrgSyncRun 追加一条运行流水。
func (d *DB) InsertOrgSyncRun(ctx context.Context, r *OrgSyncRun) (int64, error) {
	res, err := d.ExecContext(ctx, `
INSERT INTO t_org_sync_run
  (run_at, trigger, result, dept_added, dept_updated, dept_soft_deleted,
   user_added, user_updated, user_soft_deleted, field_gaps_json, error, duration_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		fmtTime(r.RunAt), r.Trigger, r.Result,
		r.DeptAdded, r.DeptUpdated, r.DeptSoftDeleted,
		r.UserAdded, r.UserUpdated, r.UserSoftDeleted,
		defaultStr(r.FieldGapsJSON, "{}"), r.Error, r.DurationMS)
	if err != nil {
		return 0, fmt.Errorf("store: 写入同步流水失败: %w", err)
	}
	return res.LastInsertId()
}
