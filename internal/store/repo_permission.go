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
func (d *DB) GetUserRole(ctx context.Context, openID string) (*UserRole, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, open_id, COALESCE(name,''), role, COALESCE(department,''), COALESCE(extra_depts,'[]'),
       active, updated_at
FROM t_user_role WHERE open_id = ? AND active = 1`, openID)
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
