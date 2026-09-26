// Package permission 实现行级过滤 + 列级投影策略引擎（M5，配置驱动）。
//
// ★ 口径不硬编码（ADR-05）：具体「角色-资源-列」对应关系来自 t_permission_rule 数据行；
// 代码只实现「读规则 → 应用策略」。Q3 定案后仅改数据行，不改代码。
package permission

import (
	"sort"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// RowScope 行级范围令牌（架构 §5.2）。
type RowScope string

const (
	ScopeSelf         RowScope = "SELF"         // 仅本人发起 / 经办
	ScopeDept         RowScope = "DEPT"         // 本部门（含分管部门）
	ScopeChargeDept   RowScope = "CHARGE_DEPT"  // 所分管部门
	ScopeAll          RowScope = "ALL"          // 全量
	ScopeAssigned     RowScope = "ASSIGNED"     // 本人被指定经办
	ScopeParticipated RowScope = "PARTICIPATED" // 本人参与验收
	ScopeDeny         RowScope = "DENY"         // 默认拒绝（deny by default）
)

// Identity 会话身份（角色每请求实时解析，不缓存决策，TC-11）。
type Identity struct {
	OpenID     string
	Role       string
	Department string
	ExtraDepts []string
}

// Condition 行级过滤条件（SQL 片段 + 参数），供仓储拼装 WHERE。
type Condition struct {
	SQL  string
	Args []any
}

// Rule 权限规则（由 t_permission_rule 装载）。
type Rule struct {
	Resource       string
	Role           string
	RowScope       RowScope
	ColumnAllow    []string
	ColumnDeny     []string
	WritableFields []string
}

// RuleFromStore 将存储层规则映射为策略规则。
func RuleFromStore(r *store.PermissionRule) Rule {
	if r == nil {
		return Rule{RowScope: ScopeDeny}
	}
	scope := RowScope(strings.ToUpper(strings.TrimSpace(r.RowScope)))
	if scope == "" {
		scope = ScopeDeny
	}
	return Rule{
		Resource:       r.Resource,
		Role:           r.Role,
		RowScope:       scope,
		ColumnAllow:    r.ColumnAllow,
		ColumnDeny:     r.ColumnDeny,
		WritableFields: r.WritableFields,
	}
}

// RowFilter 依据 row_scope 令牌生成 WHERE 片段（针对表别名 alias 的台账存档表结构）。
// 关键纪律：行过滤必须发生在 SQL 层（架构 §5.2 要点 1）。
func RowFilter(alias string, scope RowScope, id Identity) Condition {
	if alias == "" {
		alias = "a"
	}
	col := func(name string) string { return alias + "." + name }

	me := strings.TrimSpace(id.OpenID)
	switch scope {
	case ScopeAll:
		return Condition{SQL: "1=1"}

	case ScopeSelf:
		if me == "" {
			return Condition{SQL: "1=0"}
		}
		return Condition{SQL: col("applicant_open_id") + " = ?", Args: []any{me}}

	case ScopeDept:
		depts := uniqueNonEmpty(append([]string{id.Department}, id.ExtraDepts...))
		if len(depts) == 0 {
			return Condition{SQL: "1=0"}
		}
		return Condition{SQL: col("department") + " IN (" + placeholders(len(depts)) + ")", Args: toAny(depts)}

	case ScopeChargeDept:
		depts := uniqueNonEmpty(id.ExtraDepts)
		if len(depts) == 0 {
			return Condition{SQL: "1=0"}
		}
		return Condition{SQL: col("department") + " IN (" + placeholders(len(depts)) + ")", Args: toAny(depts)}

	case ScopeAssigned:
		if me == "" {
			return Condition{SQL: "1=0"}
		}
		// 指定经办人位于运营/扩展字段（口径 Q14 待定）；此处按约定键读取。
		return Condition{SQL: "json_extract(" + col("ext_json") + ",'$.assigned_open_id') = ?", Args: []any{me}}

	case ScopeParticipated:
		if me == "" {
			return Condition{SQL: "1=0"}
		}
		return Condition{SQL: "json_extract(" + col("ext_json") + ",'$.acceptors') LIKE ?", Args: []any{"%" + me + "%"}}

	default: // ScopeDeny 或未知 → 拒绝
		return Condition{SQL: "1=0"}
	}
}

// RowFilterForInstances 针对 t_instance（无 ext_json 列）的行过滤。
// ASSIGNED / PARTICIPATED 依赖运营扩展字段，在实例主表不适用，一律拒绝（1=0）。
func RowFilterForInstances(scope RowScope, id Identity) Condition {
	switch scope {
	case ScopeAssigned, ScopeParticipated:
		return Condition{SQL: "1=0"}
	default:
		return RowFilter("", scope, id)
	}
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func uniqueNonEmpty(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
