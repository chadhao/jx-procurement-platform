package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ---------- 系统管理后台（M5 / FR-M5-09~11） ----------

// GetUserRoleAny 按 open_id 读取角色，不区分启用状态。
//
// 业务侧鉴权一律走 GetUserRole（active=1，deny by default，TC-32）；
// 本方法仅供「系统管理·人员角色」页维护使用（需能看到已停用记录）。
func (d *DB) GetUserRoleAny(ctx context.Context, openID string) (*UserRole, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, open_id, COALESCE(name,''), role, COALESCE(department,''), COALESCE(extra_depts,'[]'),
       active, updated_at
FROM t_user_role WHERE open_id = ?`, openID)
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

// InsertPermissionRuleIfAbsent 仅当 (resource, role) 不存在时插入（INSERT OR IGNORE）。
//
// 用于默认口径播种（Q3）：可重复执行，且绝不覆盖管理员已修改的规则行（配置驱动，ADR-05）。
func (d *DB) InsertPermissionRuleIfAbsent(ctx context.Context, r PermissionRule) (bool, error) {
	res, err := d.ExecContext(ctx, `
INSERT OR IGNORE INTO t_permission_rule
  (resource, role, row_scope, column_allow, column_deny, writable_fields, effective_from, remark, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
		r.Resource, r.Role, r.RowScope, nullStr(marshalStrings(r.ColumnAllow)),
		nullStr(marshalStrings(r.ColumnDeny)), nullStr(marshalStrings(r.WritableFields)),
		nullStr(fmtMaybeTime(r.EffectiveFrom)), nullStr(r.Remark), fmtTime(timeNow().UTC()))
	if err != nil {
		return false, fmt.Errorf("store: 播种权限规则失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReplacePermissionRules 以整表覆盖方式保存权限矩阵（按 resource × role 覆盖保存）。
//
// 语义：提交的 rules 即最终表内容——先清空 t_permission_rule 再逐行写入，全程单事务（全成或全败）。
// 调用方负责在成功后调用 permission.Loader.Invalidate() 使规则即时生效（TC-33）。
func (d *DB) ReplacePermissionRules(ctx context.Context, rules []PermissionRule) (int, error) {
	inserted := 0
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM t_permission_rule`); err != nil {
			return fmt.Errorf("store: 清空权限规则失败: %w", err)
		}
		now := fmtTime(timeNow().UTC())
		for i := range rules {
			r := rules[i]
			if _, err := tx.ExecContext(ctx, `
INSERT INTO t_permission_rule
  (resource, role, row_scope, column_allow, column_deny, writable_fields, effective_from, remark, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
				r.Resource, r.Role, r.RowScope, nullStr(marshalStrings(r.ColumnAllow)),
				nullStr(marshalStrings(r.ColumnDeny)), nullStr(marshalStrings(r.WritableFields)),
				nullStr(fmtMaybeTime(r.EffectiveFrom)), nullStr(r.Remark), now); err != nil {
				return fmt.Errorf("store: 写入权限规则 %s/%s 失败: %w", r.Resource, r.Role, err)
			}
			inserted++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// CountPermissionRules 返回权限规则总数（供播种前后对比）。
func (d *DB) CountPermissionRules(ctx context.Context) (int, error) {
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_permission_rule`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
