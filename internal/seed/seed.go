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
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// Roles / Resources —— **权威枚举在 internal/permission**（A4：枚举归权限域）；
// 本包仅以引用方式持有（播种数据与枚举同源，不复制第二份）。
var (
	Roles     = permission.Roles
	Resources = permission.Resources
)

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
		// ★ N-061 T1（与 H3.2 同类真缺）：L01 实例级台账三可写列齐 ——
		//   `核销后余额`（spec writable:true · writer=综合运营主管 · when=月度核销时）
		//   此前独缺 ⇒ 界面按规格 label 写会 403；同批登记 sample ＋ 0021 幂等追加。
		"付款凭据号", "抽查状态", "核销后余额",
		"经办状态", "完成日期", "是否按期",
		"预付比例", "质保金余额", "结算状态", "移交集团日期",
		// ★ N-060 H3.1（R-20#13 权威名对齐 —— 旧名「提交日期」/「集团受理编号」
		//   界面按规格名写入会 403，机检 TestSeedWritableCoversSpecL06 抓取）：
		"提交集团日期", "集团流程编号",
		"集团流程状态",
		// ★ H3.3：由「付款完成日期」改名对齐 spec/ledger-mapping label。
		"付款 / 报销完成日期",
		// ★ H3.2：L06 存量可写但未登记三键中的两条（第三条＝上面的付款改名）：
		"移交凭证（签收）", "驳回原因与处置",
		// ★ N-060 H1（R-35）：合同/发票原件登记两键（中文 —— permission.CanWrite 精确匹配）。
		"原件移交清单", "原件签收记录",
		"检验结论", "差异说明", "是否已核销闭合",
		// ★ 已移除「实际到货 / 延期天数」：二者属 L11 订单执行台账，而 L11 是**派生视图、无写入口**
		//   （Q14 定案 + config.ReadOnlyLedgerTypes）。留在可写清单里会让人以为可以人工补录。
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
	// ★ N-075：原「系统管理员 {scopeAll, deny: amountDeny}」条目已移除 —— 系统角色
	//   **不进权限矩阵**（Q2：只管管理域、不参与业务可见性）。既有库中该角色的规则行
	//   迁移 0022 后无任何 Role 可匹配（t_user_role 已无该值）⇒ 沉睡行、无消费方。
}

// auditRoles 播种期允许查询审计日志的**审批角色**白名单（API §3.7：项目总经理只读）。
// ★ N-075：原「系统管理员」键已移出 —— 它不再是审批角色（Roles 无此值，本 map 只在
//
//	DefaultRules 按 Roles 迭代时被查）；系统角色的审计查询放行在**运行期**做
//	（httpapi.handleAuditLogs：矩阵非 deny ∪ SysRoles 含系统管理员 —— Role ∪ SysRoles）。
var auditRoles = map[string]bool{"项目总经理": true}

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

// SeedBootstrapSysAdmin 初始系统管理员（N-075 Q3 推荐②）：openID 取自
// JX_BOOTSTRAP_SYS_ADMIN_OPEN_ID，**缺省（空）不种** —— 否则全新库无人可进管理域
// ⇒「装完不能管」。只写 t_sys_role（系统角色独立成表，不占 t_user_role 的
// open_id UNIQUE）；幂等（ON CONFLICT 冲突即更新，重复执行结果一致）。
func SeedBootstrapSysAdmin(ctx context.Context, db *store.DB, openID string) (bool, error) {
	openID = strings.TrimSpace(openID)
	if openID == "" {
		return false, nil
	}
	if err := db.UpsertSysRole(ctx, store.SysRole{
		OpenID: openID, Role: permission.SysRoles[0], Active: true,
	}); err != nil {
		return false, fmt.Errorf("seed: 播种初始系统管理员失败: %w", err)
	}
	return true, nil
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
