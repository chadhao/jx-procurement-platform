package store

import (
	"context"
	"fmt"
)

// ChainRoleCandidate 链计算用的候选审批人（store 侧形态；
// 由 cmd/jxapproval 装配层适配为 chain.RoleSource —— store 不得 import 上层包）。
type ChainRoleCandidate struct {
	OpenID string
	Name   string
}

// ChainRoleCandidates 按**中文角色名**取在岗候选审批人。
//
// 过滤纪律：
//   - t_user_role.active=1（权限源 —— 「镜像≠权限」，镜像只做过滤）；
//   - 镜像行存在时要求在用：is_deleted/is_resigned/is_exited/is_frozen 全为 0
//     （镜像缺失**不剔除** —— orgsync 未跑过时不至于把全部候选滤空）；
//   - matchDept 时附加「department 相等 或 extra_depts（JSON 数组文本）包含该部门」。
//     ★ extra_depts 用 LIKE 子串匹配 JSON 文本，部门名互为前缀时有极小误配面
//     （现部门名单无此情形；登记为已知代价）。
func (d *DB) ChainRoleCandidates(ctx context.Context, role, department string, matchDept bool) ([]ChainRoleCandidate, error) {
	query := `
SELECT u.open_id, COALESCE(u.name, '')
FROM t_user_role u
LEFT JOIN t_org_user o ON o.open_id = u.open_id
WHERE u.role = ?
  AND u.active = 1
  AND (o.open_id IS NULL
       OR (o.is_deleted = 0 AND o.is_resigned = 0 AND o.is_exited = 0 AND o.is_frozen = 0))`
	args := []any{role}
	if matchDept {
		query += ` AND (u.department = ? OR u.extra_depts LIKE '%' || ? || '%')`
		args = append(args, department, department)
	}
	query += ` ORDER BY u.open_id`

	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询角色候选 %q: %w", role, err)
	}
	defer func() { _ = rows.Close() }()

	out := []ChainRoleCandidate{}
	for rows.Next() {
		var c ChainRoleCandidate
		if err := rows.Scan(&c.OpenID, &c.Name); err != nil {
			return nil, fmt.Errorf("store: 扫描角色候选行: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
