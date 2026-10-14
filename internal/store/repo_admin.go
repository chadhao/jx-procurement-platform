package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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

// AdminUserRow 「人员角色」页的行视图：**以通讯录镜像 t_org_user 为基**（在册者全覆盖），
// 角色 / 分管部门 / 启用态 / 更新时间**左连** t_user_role 借用（未绑角色者这些字段为空值——
// **不编造**时间、**不伪造**角色）。
//
// ★ 姓名 / 主部门**一律取自镜像**（飞书权威），**不读** t_user_role 的同名列：
//
//	用户口径（2026-10-14）「姓名和主部门需保持和飞书一致，不允许在后台修改」
//	⇒ 后台列表若读角色表的历史人工值，就会把降级数据当主数据展示。
type AdminUserRow struct {
	OpenID     string
	Name       string     // 镜像姓名（飞书权威）
	Role       string     // 未绑角色 = ""（前端显示「未分配」）
	Department string     // 主部门名（镜像按 primary_department_id 解析；根部门/空部门 = ""）
	ExtraDepts []string   // 未绑角色 = 空
	Active     *bool      // 未绑角色 = nil（无启用态，非"停用"）
	UpdatedAt  *time.Time // 未绑角色 = nil（不编造更新时间）
}

// ListAdminUsers 列出「通讯录在册者」的人员角色视图（migration 0012 镜像为基）。
//
// ★ 过滤口径 `is_deleted=0 AND is_resigned=0` 与「在用」判定一致（deny-by-default 的
// 前提是"人还在册"）：离职/退出者 orgsync 已置 is_deleted=1，双重条件使口径显式、不随
// 同步实现细节漂移。★ 未绑角色者**仍返回**（role 空）——这正是本接口存在的理由：
// 让同步进来的新人**能被看到、能被分配角色**（否则同步报告说"N 人"、列表却少于 N）。
func (d *DB) ListAdminUsers(ctx context.Context) ([]AdminUserRow, error) {
	rows, err := d.QueryContext(ctx, `
SELECT u.open_id, COALESCE(u.name,''), COALESCE(r.role,''), COALESCE(g.name,''),
       COALESCE(r.extra_depts,'[]'), r.active, r.updated_at
FROM t_org_user u
LEFT JOIN t_org_department g ON g.open_department_id = u.primary_department_id
LEFT JOIN t_user_role r      ON r.open_id = u.open_id
WHERE u.is_deleted = 0 AND u.is_resigned = 0
ORDER BY u.open_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AdminUserRow{}
	for rows.Next() {
		var (
			r       AdminUserRow
			extra   string
			active  sql.NullInt64
			updated sql.NullString
		)
		if err := rows.Scan(&r.OpenID, &r.Name, &r.Role, &r.Department, &extra, &active, &updated); err != nil {
			return nil, err
		}
		r.ExtraDepts = unmarshalStrings(extra)
		if active.Valid { // 有角色行才有启用态
			v := active.Int64 == 1
			r.Active = &v
		}
		if updated.Valid && strings.TrimSpace(updated.String) != "" {
			t := parseTime(updated.String)
			r.UpdatedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetOrgUserDisplay 按 open_id 取通讯录镜像的展示信息（姓名 + 主部门名 + 是否在用）。
//
// ★ 用途：未绑角色者**首次分配角色**时，以**飞书权威值**播种 t_user_role 的
// name/department 两列（t_user_role 仍是若干历史展示路径的数据源——见 assigneeRoleViews；
// 播种为空会把这些路径的姓名/部门打空）。★ 不编造：镜像无行 ⇒ ErrNotFound。
// ★ InService=is_deleted=0 ∧ is_resigned=0（离职/注销者不得被分配角色）。
func (d *DB) GetOrgUserDisplay(ctx context.Context, openID string) (name, department string, inService bool, err error) {
	row := d.QueryRowContext(ctx, `
SELECT COALESCE(u.name,''), COALESCE(g.name,''), u.is_deleted, u.is_resigned
FROM t_org_user u
LEFT JOIN t_org_department g ON g.open_department_id = u.primary_department_id
WHERE u.open_id = ?`, openID)
	var deleted, resigned int
	if err := row.Scan(&name, &department, &deleted, &resigned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", false, ErrNotFound
		}
		return "", "", false, err
	}
	return name, department, deleted == 0 && resigned == 0, nil
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
