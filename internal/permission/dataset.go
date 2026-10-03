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
		// ★ N-042：0019 补列后改读**规范列**（docs/19 §3.3「台账侧同款」）——
		//   精确等值；open_id 存在前缀包含关系（ou_ab 是 ou_abc 前缀），绝不用 LIKE。
		//   历史行（列 NULL）不命中＝fail-closed（宁可少不可多）。
		return Condition{SQL: col("designated_open_id") + " = ?", Args: []any{me}}

	case ScopeParticipated:
		if me == "" {
			return Condition{SQL: "1=0"}
		}
		// ★ N-042：规范列 acceptors（JSON 数组串或普通文本）——逐元素精确匹配，绝不用 LIKE。
		return Condition{SQL: textOrJSONContains(col("acceptors")), Args: []any{me, me}}

	default: // ScopeDeny 或未知 → 拒绝
		return Condition{SQL: "1=0"}
	}
}

// jsonScalarEquals 生成「JSON 标量字段等于 :me」的谓词，占位符恰好 1 个。
//
// ★ 为什么必须套 `CASE WHEN json_valid(col)`：SQLite 的 JSON 函数遇到非法 JSON 会抛
// `SQL logic error: malformed JSON` —— 这是**整条查询失败**（而非仅该行被过滤）。
// 即 `ext_json` 里只要有一行被写脏，整个台账列表/看板/实例查询就全部 500。
// 用 `CASE` 而非 `AND` 是因为 SQL 不保证 `AND` 操作数的求值顺序，`CASE` 才有条件求值保证。
func jsonScalarEquals(jsonCol, path string) string {
	return "(CASE WHEN json_valid(" + jsonCol + ") THEN json_extract(" + jsonCol + ",'" + path +
		"') = ? ELSE 0 END)"
}

// jsonArrayContains 生成「JSON 数组字段含 :me 元素」的**精确匹配**谓词，占位符恰好 1 个。
//
// `json_each(col,'$.key')` 对标量字符串、数组、键不存在、列为 NULL 均安全
// （分别是 1 行 / 逐元素 / 0 行 / 0 行，均不报错）。
//
// ★ 非法 JSON 的处置是 `ELSE 0`（**不放行**）——历史注释曾写成“退化为整体等值比较”，
//
//	与实现相反。以代码为准：脏 JSON 行**不参与匹配**（fail-closed）。
//	若确需兼容普通文本列，请用 `textOrJSONContains`（它对非法 JSON 走整体等值，占位符 2 个）。
func jsonArrayContains(jsonCol, path string) string {
	return "(CASE WHEN json_valid(" + jsonCol + ") THEN EXISTS (SELECT 1 FROM json_each(" +
		jsonCol + ",'" + path + "') WHERE json_each.value = ?) ELSE 0 END)"
}

// textOrJSONContains 生成「可能是 JSON 数组、也可能是普通文本」的列的**精确匹配**谓词，占位符 2 个。
//
// ★ 用于 t_submission 的 `assigned_open_id` / `acceptors` 这类**普通 TEXT 列**（非 ext_json）：
//
//	JSON 合法 → `json_each(col,'$')` 展开后逐元素比较（数组、标量、对象成员均可）；
//	非法/普通文本 → 退化为**整体等值比较**（宁可少不可多，绝不越权）。
//
// ★ 仍然绝不用 LIKE：open_id 之间存在前缀包含关系（`ou_ab` 是 `ou_abc` 的前缀），
//
//	子串匹配会把「只含 ou_abc」的记录判给 `ou_ab` → 越权可见。
func textOrJSONContains(col string) string {
	return "(CASE WHEN json_valid(" + col + ") THEN EXISTS (SELECT 1 FROM json_each(" +
		col + ",'$') WHERE json_each.value = ?) ELSE " + col + " = ? END)"
}

// RowFilterForInstances 针对 t_instance（规范列承载）的行过滤。
// ★ N-042：0019 补列后 ASSIGNED/PARTICIPATED 不再 1=0 ——
//
//	ASSIGNED     → designated_open_id = me（精确等值；open_id 有前缀包含关系，绝不 LIKE）
//	PARTICIPATED → textOrJSONContains("acceptors")（JSON 数组或普通文本均精确匹配）
//	空身份仍 fail-closed（1=0）。
func RowFilterForInstances(scope RowScope, id Identity) Condition {
	me := strings.TrimSpace(id.OpenID)
	switch scope {
	case ScopeAssigned:
		if me == "" {
			return Condition{SQL: "1=0"}
		}
		return Condition{SQL: "designated_open_id = ?", Args: []any{me}}
	case ScopeParticipated:
		if me == "" {
			return Condition{SQL: "1=0"}
		}
		return Condition{SQL: textOrJSONContains("acceptors"), Args: []any{me, me}}
	default:
		return RowFilter("", scope, id)
	}
}

// RowFilterForSubmission 针对 t_submission（报送登记）的行过滤。
//
// ★ 为什么必须收敛：异常面板的「集团驳回后未处置」等指标原为**全表 COUNT(*)**，
// 而默认种子把 `dashboard:4` 发给了**全部 10 个角色**（含申请人 SELF、采购经办人 ASSIGNED、
// 验收人 PARTICIPATED）→ 任何能打开该看板的角色都会拿到自己无权查看的**全局聚合值**：
// 既是行级越权，也让「指标数字」与「他实际能看到的数据」互相矛盾（对账时必然对不上）。
//
// ★ Q14-B 第 5 项（2026-09-26 决定「按真实列重建」）：migrations/0003 为 t_submission 补上了
// `department` / `applicant_open_id` / `assigned_open_id` / `acceptors` 四列，故四个令牌
// **不再降级为 `created_by = me`**（原降级见 docs/06 B21），恢复其应有语义：
//
//	SELF          → applicant_open_id = me
//	DEPT          → department IN (本人部门 + 兼任部门)
//	CHARGE_DEPT   → department IN (分管部门)
//	ASSIGNED      → assigned_open_id 含 me（标量或数组均可，精确匹配）
//	PARTICIPATED  → acceptors 含 me（同上）
//
// 未知 / DENY / 空身份 → `1=0`（fail-closed，绝不越权）。
func RowFilterForSubmission(scope RowScope, id Identity, alias string) Condition {
	if alias == "" {
		alias = "s"
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
		// 与台账侧同口径：**逐元素精确匹配**，兼容 JSON 数组与普通文本两种存量形态；
		// 绝不用 LIKE（open_id 存在前缀包含关系，子串匹配会越权）。占位符 2 个。
		return Condition{SQL: textOrJSONContains(col("assigned_open_id")), Args: []any{me, me}}

	case ScopeParticipated:
		if me == "" {
			return Condition{SQL: "1=0"}
		}
		return Condition{SQL: textOrJSONContains(col("acceptors")), Args: []any{me, me}}

	default: // DENY 或未知 → 拒绝
		return Condition{SQL: "1=0"}
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
