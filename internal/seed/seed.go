// Package seed 落地「可重复执行」的默认数据。
//
// 本次只做一件事：把 PRD §4.2 / 架构 §5.4 的行·列权限建议口径固化为 t_permission_rule 数据行
// （Q3 定案 2026-09-26）。采用「独立子命令 + 启动时幂等播种」而非迁移脚本，理由见
// docs/06-Implementation-Notes.md §G：
//   - 迁移保持纯 DDL，不掺业务数据；
//   - INSERT OR IGNORE 保证可重复执行且绝不覆盖管理员已改动的规则行（配置驱动，ADR-05）；
//   - 可用 `jxapproval seed` 手动重跑。
package seed

import (
	"context"
	"fmt"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// Roles 角色枚举（PRD §4.1；采购岗 / 财务岗 / 出纳已取消，不得出现）。
var Roles = []string{
	"申请人", "主管领导", "项目总经理", "副总", "综合运营主管",
	"采购经办人", "验收人", "集团财务", "集团（审批）", "系统管理员",
}

// Resources 权限矩阵覆盖的资源枚举。
// 至少覆盖 ledger:* / dashboard:1..4 / api:instances（API §3.9）；另补 api:audit 供审计页使用。
var Resources = []string{
	"ledger:*",
	"dashboard:1", "dashboard:2", "dashboard:3", "dashboard:4",
	"api:instances",
	"api:audit",
}

// 行范围令牌（架构 §5.2）：6 令牌 + DENY，代码与数据均只用这 7 个值，不接受条件表达式。
const (
	scopeSelf         = "SELF"
	scopeDept         = "DEPT"
	scopeChargeDept   = "CHARGE_DEPT"
	scopeAll          = "ALL"
	scopeAssigned     = "ASSIGNED"
	scopeParticipated = "PARTICIPATED"
	scopeDeny         = "DENY"
)

// 金额列（含公式红标内的嵌套金额，避免列投影在顶层裁剪后仍从 formula_flags 泄漏）。
var amountDeny = []string{"amount_cents", "amount_display", "formula_flags"}

// 集团侧人工登记列（FR-M6-07：仅人工登记、不回传）。
var groupManualDeny = []string{"grp_accept_no", "grp_state", "paid_date", "paid_cents", "reject_reason"}

// 运营表可写字段（Q14 待确认，此处按架构 §3.4 台账可写字段的建议键占位）。
var (
	opsSupervisor = []string{
		"付款凭据号", "抽查状态", "经办状态", "完成日期", "是否按期",
		"预付比例", "质保金余额", "结算状态", "移交集团日期",
		"提交日期", "集团受理编号", "集团流程状态", "付款完成日期",
		"检验结论", "差异说明", "是否已核销闭合", "实际到货", "延期天数",
	}
	opsHandler  = []string{"经办状态", "完成日期", "是否按期"}
	opsAcceptor = []string{"检验结论", "差异说明"}
)

// roleSpec 单角色默认口径（PRD §4.2 / 架构 §5.4）。
type roleSpec struct {
	rowScope string
	deny     []string
	writable []string
}

// specs 角色 → 默认口径。row_scope 之外的列可见性完全由数据行决定（不硬编码进代码）。
var specs = map[string]roleSpec{
	"申请人":    {rowScope: scopeSelf, deny: groupManualDeny},
	"主管领导":   {rowScope: scopeDept},
	"项目总经理":  {rowScope: scopeAll},
	"副总":     {rowScope: scopeChargeDept},
	"综合运营主管": {rowScope: scopeAll, writable: opsSupervisor},
	"采购经办人":  {rowScope: scopeAssigned, deny: append(append([]string{}, amountDeny...), groupManualDeny...), writable: opsHandler},
	"验收人":    {rowScope: scopeParticipated, deny: amountDeny, writable: opsAcceptor},
	"集团财务":   {rowScope: scopeDeny},
	"集团（审批）": {rowScope: scopeDeny},
	"系统管理员":  {rowScope: scopeAll, deny: amountDeny}, // 全量只读、不含金额列（定案）
}

// auditRoles 允许查询审计日志的角色（API §3.7：系统管理员；项目总经理只读）。
var auditRoles = map[string]bool{"系统管理员": true, "项目总经理": true}

// DefaultRules 返回默认权限矩阵（角色 × 资源）的全部规则行。
func DefaultRules() []store.PermissionRule {
	rules := make([]store.PermissionRule, 0, len(Roles)*len(Resources))
	for _, role := range Roles {
		sp := specs[role]
		scope := sp.rowScope
		if scope == "" {
			scope = scopeDeny
		}
		for _, res := range Resources {
			ruleScope := scope
			// 审计资源配置单独收敛：仅系统管理员与项目总经理可见，其余默认拒绝。
			if res == "api:audit" && !auditRoles[role] {
				ruleScope = scopeDeny
			}
			rules = append(rules, store.PermissionRule{
				Resource:       res,
				Role:           role,
				RowScope:       ruleScope,
				ColumnDeny:     cloneStrings(sp.deny),
				WritableFields: cloneStrings(sp.writable),
				Remark:         "Q3 默认口径（PRD §4.2 / 架构 §5.4），改数据即生效",
			})
		}
	}
	return rules
}

// SeedQ3Defaults 幂等播种默认权限矩阵；返回本次新插入的行数（已存在的不覆盖）。
func SeedQ3Defaults(ctx context.Context, db *store.DB) (int, error) {
	rules := DefaultRules()
	inserted := 0
	for _, r := range rules {
		ok, err := db.InsertPermissionRuleIfAbsent(ctx, r)
		if err != nil {
			return inserted, fmt.Errorf("seed: 播种权限规则失败: %w", err)
		}
		if ok {
			inserted++
		}
	}
	return inserted, nil
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
