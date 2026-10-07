package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---------- 用户角色映射（M5） ----------

// GetUserRole 按 open_id 读取角色；未映射或已停用返回 ErrNotFound（deny by default，TC-32）。
// GetUserRoleTx 同 GetUserRole（事务内）—— flow 指定经办范围标记在 WithTx 内查部门用。
func (d *DB) GetUserRoleTx(ctx context.Context, tx *sql.Tx, openID string) (*UserRole, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, open_id, COALESCE(name,''), role, COALESCE(department,''), COALESCE(extra_depts,'[]'),
       active, updated_at
FROM t_user_role WHERE open_id = ? AND active = 1`, openID)
	return scanUserRoleRow(row)
}

func (d *DB) GetUserRole(ctx context.Context, openID string) (*UserRole, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, open_id, COALESCE(name,''), role, COALESCE(department,''), COALESCE(extra_depts,'[]'),
       active, updated_at
FROM t_user_role WHERE open_id = ? AND active = 1`, openID)
	return scanUserRoleRow(row)
}

// scanUserRoleRow 单行扫描（非 Tx / Tx 两条查询共用，防两份扫描逻辑漂移）。
func scanUserRoleRow(row *sql.Row) (*UserRole, error) {
	var (
		r       UserRole
		extra   string
		active  int
		updated string
	)
	if err := row.Scan(&r.ID, &r.OpenID, &r.Name, &r.Role, &r.Department, &extra, &active, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.ExtraDepts = unmarshalStrings(extra)
	r.Active = active == 1
	r.UpdatedAt = parseTime(updated)
	return &r, nil
}

// UpsertUserRole 写入/更新用户角色映射。
func (d *DB) UpsertUserRole(ctx context.Context, r UserRole) error {
	active := 0
	if r.Active {
		active = 1
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO t_user_role (open_id, name, role, department, extra_depts, active, updated_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(open_id) DO UPDATE SET
  name = excluded.name, role = excluded.role, department = excluded.department,
  extra_depts = excluded.extra_depts, active = excluded.active, updated_at = excluded.updated_at`,
		r.OpenID, nullStr(r.Name), r.Role, nullStr(r.Department), defaultStr(marshalStrings(r.ExtraDepts), "[]"),
		active, fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 写入用户角色失败: %w", err)
	}
	return nil
}

// SysRole 系统角色行（t_sys_role，N-075）：与审批角色（t_user_role）解耦 ——
// 系统角色可叠加、可与任一审批角色共存（UNIQUE(open_id, role)）。
type SysRole struct {
	OpenID string
	Role   string
	Active bool
}

// UpsertSysRole 写入/更新系统角色映射（幂等；bootstrap 初始管理员与测试夹具共用）。
func (d *DB) UpsertSysRole(ctx context.Context, r SysRole) error {
	active := 0
	if r.Active {
		active = 1
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO t_sys_role (open_id, role, active, updated_at)
VALUES (?,?,?,?)
ON CONFLICT(open_id, role) DO UPDATE SET
  active = excluded.active, updated_at = excluded.updated_at`,
		r.OpenID, r.Role, active, fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 写入系统角色失败: %w", err)
	}
	return nil
}

// GetSysRoles 读取某 open_id 的**启用中**系统角色（口径比照 GetUserRole：active=1）。
// ★ 未映射 ⇒ 空集、nil 错误（deny by default 不变 —— 调用方按「有无系统角色」自行判定）。
func (d *DB) GetSysRoles(ctx context.Context, openID string) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
SELECT role FROM t_sys_role WHERE open_id = ? AND active = 1 ORDER BY role`, openID)
	if err != nil {
		return nil, fmt.Errorf("store: 读取系统角色失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, fmt.Errorf("store: 读取系统角色失败: %w", err)
		}
		out = append(out, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 读取系统角色失败: %w", err)
	}
	return out, nil
}

// ListUserRoles 列出全部用户角色。
func (d *DB) ListUserRoles(ctx context.Context) ([]UserRole, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id, open_id, COALESCE(name,''), role, COALESCE(department,''), COALESCE(extra_depts,'[]'),
       active, updated_at FROM t_user_role ORDER BY open_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []UserRole
	for rows.Next() {
		var (
			r       UserRole
			extra   string
			active  int
			updated string
		)
		if err := rows.Scan(&r.ID, &r.OpenID, &r.Name, &r.Role, &r.Department, &extra, &active, &updated); err != nil {
			return nil, err
		}
		r.ExtraDepts = unmarshalStrings(extra)
		r.Active = active == 1
		r.UpdatedAt = parseTime(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MapUserRolesByOpenIDs 批量读取多个 open_id 的角色映射（**含已停用**：本方法只服务
// 「展示用」场景——历史任务办理人姓名/部门不因停用而消失；鉴权仍走 GetUserRole）。
//
// ★ 批量语义（避免 N+1）：调用方把一批任务的所有 assignee open_id 一次传入，
// 本方法单条 IN 查询取回；查不到的 open_id **不在返回 map 中**——调用方按
// 「字段留空」处理，**不得**把 open_id 塞进 name（前端回落 `-`）。
func (d *DB) MapUserRolesByOpenIDs(ctx context.Context, openIDs []string) (map[string]UserRole, error) {
	out := make(map[string]UserRole, len(openIDs))
	// 去重 + 去空：防拼接出畸形 IN 列表，也压缩查询规模。
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
SELECT id, open_id, COALESCE(name,''), role, COALESCE(department,''), COALESCE(extra_depts,'[]'),
       active, updated_at
FROM t_user_role WHERE open_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r       UserRole
			extra   string
			active  int
			updated string
		)
		if err := rows.Scan(&r.ID, &r.OpenID, &r.Name, &r.Role, &r.Department, &extra, &active, &updated); err != nil {
			return nil, err
		}
		r.ExtraDepts = unmarshalStrings(extra)
		r.Active = active == 1
		r.UpdatedAt = parseTime(updated)
		out[r.OpenID] = r
	}
	return out, rows.Err()
}

// ListDistinctDepartments 返回 `t_user_role.department` 的**非空去重**清单（稳定排序＝字典序）。
//
// ★ 数据源边界：部门清单同样来自**角色表**（＝已配置角色者的部门），**不是**飞书
// 通讯录组织架构 —— 同 handlers_org.go 的边界说明。表为空 ⇒ 返回空切片（不报错）。
func (d *DB) ListDistinctDepartments(ctx context.Context) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
SELECT DISTINCT department FROM t_user_role
WHERE department IS NOT NULL AND TRIM(department) <> ''
ORDER BY department`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var dept string
		if err := rows.Scan(&dept); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(dept))
	}
	return out, rows.Err()
}

// ---------- 权限规则（配置驱动，M5） ----------

// GetPermissionRule 读取 (resource, role) 权限规则；不存在返回 ErrNotFound。
func (d *DB) GetPermissionRule(ctx context.Context, resource, role string) (*PermissionRule, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, resource, role, row_scope, COALESCE(column_allow,''), COALESCE(column_deny,''),
       COALESCE(writable_fields,''), COALESCE(effective_from,''), COALESCE(remark,''), updated_at
FROM t_permission_rule WHERE resource = ? AND role = ?`, resource, role)
	return scanPermissionRule(row)
}

// ListPermissionRules 列出全部权限规则。
func (d *DB) ListPermissionRules(ctx context.Context) ([]PermissionRule, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id, resource, role, row_scope, COALESCE(column_allow,''), COALESCE(column_deny,''),
       COALESCE(writable_fields,''), COALESCE(effective_from,''), COALESCE(remark,''), updated_at
FROM t_permission_rule ORDER BY resource, role`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []PermissionRule
	for rows.Next() {
		r, err := scanPermissionRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// UpsertPermissionRule 写入/更新权限规则（口径变更只改数据行，不改代码，ADR-05）。
func (d *DB) UpsertPermissionRule(ctx context.Context, r PermissionRule) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_permission_rule
  (resource, role, row_scope, column_allow, column_deny, writable_fields, effective_from, remark, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(resource, role) DO UPDATE SET
  row_scope = excluded.row_scope, column_allow = excluded.column_allow,
  column_deny = excluded.column_deny, writable_fields = excluded.writable_fields,
  effective_from = excluded.effective_from, remark = excluded.remark, updated_at = excluded.updated_at`,
		r.Resource, r.Role, r.RowScope, nullStr(marshalStrings(r.ColumnAllow)),
		nullStr(marshalStrings(r.ColumnDeny)), nullStr(marshalStrings(r.WritableFields)),
		nullStr(fmtMaybeTime(r.EffectiveFrom)), nullStr(r.Remark), fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 写入权限规则失败: %w", err)
	}
	return nil
}

func scanPermissionRule(s interface {
	Scan(dest ...any) error
}) (*PermissionRule, error) {
	var (
		r        PermissionRule
		allow    string
		deny     string
		writable string
		effFrom  string
		updated  string
	)
	if err := s.Scan(&r.ID, &r.Resource, &r.Role, &r.RowScope, &allow, &deny, &writable, &effFrom, &r.Remark, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.ColumnAllow = unmarshalStrings(allow)
	r.ColumnDeny = unmarshalStrings(deny)
	r.WritableFields = unmarshalStrings(writable)
	r.EffectiveFrom = parseTimePtr(effFrom)
	r.UpdatedAt = parseTime(updated)
	return &r, nil
}

// ---------- 台账字段定义（M4） ----------

// UpsertLedgerFieldDef 写入/更新台账字段定义。
// UpsertLedgerFieldDef 写入/更新台账字段定义（独立连接）。
func (d *DB) UpsertLedgerFieldDef(ctx context.Context, f LedgerFieldDef) error {
	return upsertLedgerFieldDef(ctx, d, f)
}

// UpsertLedgerFieldDefTx 事务内写入/更新台账字段定义。
func (d *DB) UpsertLedgerFieldDefTx(ctx context.Context, tx *sql.Tx, f LedgerFieldDef) error {
	return upsertLedgerFieldDef(ctx, tx, f)
}

func upsertLedgerFieldDef(ctx context.Context, q execer, f LedgerFieldDef) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO t_ledger_field_def (ledger_type, field_key, field_label, is_formula, formula_kind, is_sensitive)
VALUES (?,?,?,?,?,?)
ON CONFLICT(ledger_type, field_key) DO UPDATE SET
  field_label = excluded.field_label, is_formula = excluded.is_formula,
  formula_kind = excluded.formula_kind, is_sensitive = excluded.is_sensitive`,
		f.LedgerType, f.FieldKey, nullStr(f.FieldLabel), boolToInt(f.IsFormula), nullStr(f.FormulaKind), boolToInt(f.IsSensitive))
	return err
}

// DeleteLedgerFieldDefsKindTx 清空全部台账字段定义，返回删除条数。
//
// 供「导入 = 全量替换」使用（与 `DeleteConfigMappingsKindTx` 同理）：
// `t_ledger_field_def` 的主键是 (ledger_type, field_key)，本身不会残留旧 value；
// 但**文件里被删掉的字段定义**若不清理，旧白名单会继续放行已废弃的键名。
func (d *DB) DeleteLedgerFieldDefsKindTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM t_ledger_field_def`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LedgerFieldKeys 返回某台账类型**已登记**的字段键集合（无登记时返回空集合，不返回错误）。
//
// 用途（Q14-B 第 4 项）：让 `PATCH /api/ledger/{table}/{id}` 用「台账字段定义」做 `fields` 的
// **键名白名单**——这样 `t_ledger_field_def` 才真正有消费端，不再是"配了没人读"的空表。
//
// ★ 语义：**该台账登记过字段定义 → 只接受登记过的键**；**未登记任何字段 → 不做键名限制**
// （保持向后兼容，避免把既有部署的写入口一次性打死）。
func (d *DB) LedgerFieldKeys(ctx context.Context, ledgerType string) (map[string]bool, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT field_key FROM t_ledger_field_def WHERE ledger_type = ?`, ledgerType)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		// ★ 键名归一（去空白 + 小写）：写侧会用同一归一后的形式比对，
		//   避免"登记时带空格/大写、写入时对不上"导致的整行写不进去（B34）。
		out[strings.ToLower(strings.TrimSpace(k))] = true
	}
	return out, rows.Err()
}

// ListLedgerFieldDefs 列出某台账类型的字段定义。
func (d *DB) ListLedgerFieldDefs(ctx context.Context, ledgerType string) ([]LedgerFieldDef, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id, ledger_type, field_key, COALESCE(field_label,''), is_formula, COALESCE(formula_kind,''), is_sensitive
FROM t_ledger_field_def WHERE ledger_type = ? ORDER BY id`, ledgerType)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []LedgerFieldDef
	for rows.Next() {
		var (
			f       LedgerFieldDef
			formula int
			sens    int
		)
		if err := rows.Scan(&f.ID, &f.LedgerType, &f.FieldKey, &f.FieldLabel, &formula, &f.FormulaKind, &sens); err != nil {
			return nil, err
		}
		f.IsFormula = formula == 1
		f.IsSensitive = sens == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

// SensitiveFields 返回某台账类型的敏感列集合（列级权限兜底，架构 §5.3）。
func (d *DB) SensitiveFields(ctx context.Context, ledgerType string) (map[string]bool, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT field_key FROM t_ledger_field_def WHERE ledger_type = ? AND is_sensitive = 1`, ledgerType)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[strings.TrimSpace(k)] = true
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
