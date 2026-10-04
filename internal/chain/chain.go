// Package chain 实现分档与审批链计算（FR-M9-02）——
// 数据源唯一权威＝spec/chain.json（经 specload.Bundle 内嵌加载，N-008 定案）。
//
// ★ 模块边界（批 1 方案 d3）：本包是**纯计算包** —— 输入 Bundle + Facts，
//
//	输出 RoleNode（角色级链）；角色→人的解析（assign.go）经 RoleSource 端口
//	取数据，不直接依赖 store。preview 与 submit 共用同一入口（D5），
//	禁止前端本地算分档。
//
// ★ 「chain 环节 → flow 审批任务」映射规则（技术口径，mimo 负责）：
//   - actor ∈ {applicant, system, purchaser} ⇒ 动作/系统环节，不生成审批任务
//     （自购、回交凭据、防拆分校验、询比价执行等），preview 中仍展示；
//   - ref == contract_two_level ⇒ 展开为合同两级（仅 supervisor_approval 与
//     pgm_approval 两个 id；draft/upload/sign/confirm_supplier 是动作或重复环节）；
//     命中 supervisor_is_pgm 分支（申请人部门 ∈ project_general_manager.
//     is_also_supervisor_for）⇒ 跳过二级；
//   - 其余 actor 若属审批角色（supervisor / project_general_manager /
//     deputy_general_manager / ops_supervisor / inspector_group）⇒ 审批任务；
//   - 未知 actor ⇒ 不生成任务（保守：宁可少推不可错推），NodeName 保留可追溯。
package chain

import (
	"errors"
	"fmt"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// 支持的单据类型（批 1 = N-005 批 1 范围：BA+SA+PR）。
const (
	DocBA = "BA"
	DocPR = "PR"
	DocSA = "SA"
	DocCT = "CT" // 合同 / 简式订单（批 3 · T4）
	DocSS = "SS" // 单一来源理由书（N-036 接线批 ②：链算通路）
	DocPC = "PC" // 采购变更单（同上）
)

// 常见错误（handler 映射为 40000 + 中文明细；错误码枚举归 WorkBuddy，N-018）。
var (
	// ErrUnsupportedDoc 批 1 之外的单据类型。
	ErrUnsupportedDoc = errors.New("chain: 暂不支持该单据类型（批 1 仅 BA/PR/SA）")
	// ErrAmountMissing 分档需要金额但未提供。
	ErrAmountMissing = errors.New("chain: 分档需要金额（amount_cents）")
	// ErrTierOutOfDoc 金额档位与单据类型不匹配（N-012 过渡：PR<1000 拒收引导走 BA）。
	ErrTierOutOfDoc = errors.New("chain: 金额档位与单据不匹配")
	// ErrRouteMissing doc_chains 缺该档位路由。
	ErrRouteMissing = errors.New("chain: doc_chains 缺该档位路由")
	// ErrTierNoBand 金额不落入任何采档区间（S7 保证区间无缝，正常不可达）。
	ErrTierNoBand = errors.New("chain: 金额不落入任何采档区间")
	// ErrCategoryInvalid 用途分类无法归线。
	ErrCategoryInvalid = errors.New("chain: 用途分类无法归入流程线")
	// ErrPaymentInvalid 费用线按支付方式二分时取值非法。
	ErrPaymentInvalid = errors.New("chain: 支付方式取值非法")
)

// Facts 计算输入（提交/预览共用的事实集合）。
type Facts struct {
	DocType            string
	AmountCents        *int64 // 分；采购线必填（费用线可空）
	UsageCategoryL1    string // 一级用途分类（P01..P08 / S01..S07 / M01..M08）
	PaymentMethodInput string
	// Department 申请人部门（中文名，与 t_user_role.department / chain roles.is_*_for 数组同口径）。
	Department string
	// ApplicantIsOpsSupervisor 驱动 R-03 备付金节点上抬分支。
	ApplicantIsOpsSupervisor bool
	// IsFixedAsset N-013（已 AGREED）：forms/PR.json 已补「是否属于固定资产类」布尔字段，
	// submit/preview 从表单取值传入 —— tier3_plus 的 `or is_fixed_asset` 分支自接线起可达。
	IsFixedAsset bool
	// HasContract T3 / R-26：该支出**是否签了合同** —— 付款路径的第 1 优先级判据，
	// 同时驱动「合同审批两级照走」（触发条件＝是否签合同，不是金额）。
	// CT 单据恒为 true（handler 侧强制）；其余单据由请求显式给出（缺省 false）。
	HasContract bool
	// ★ N-044（tier_source=r15_max）：PC 档位就高公式的两个输入 ——
	// submit 期由 injectPCSSSystemFields 反查/注入后填入；preview 期可能缺省
	//（fallback 见 resolveTierForExpand —— 如实单值回退，不静默假定）。
	ChangeAmountCents           *int64
	OriginalContractAmountCents *int64
	// ★ N-045（tier_source=emergency_max，裁定 R-31）：跨单就高的另一个输入 ——
	// 关联 PR 金额（其所属采购申请的金额，分）。与 AmountCents 的分工：
	// AmountCents ＝ 补录金额（紧急采购本单实际发生金额，提交时给出）；
	// RelatedPRAmountCents ＝ 该紧急单所属 PR 的金额（就高公式的另一侧）。
	// ★ 两值必须齐全（conventions.tier_source ③）：缺任一 ⇒ 可见失败，不单边退化
	//（「不得降档」是单调上界 —— 单边取值可能低于应属档位）。
	// ★ 本字段仅 chain 层可测：emergency 链未接线（ResolveRoute/doc_chains 无
	// emergency case，属 A8），生产者随通路批接入。
	RelatedPRAmountCents *int64
}

// RoleNode 链计算的中间产物：角色级节点（含非审批环节，供 preview 展示全流程）。
type RoleNode struct {
	Seq          int    // 审批任务序（IsApproval 时 1..N；非审批环节为 0）
	SourceNodeID string // 原始链节点 id（如 approve_petty_cash / contract_supervisor）
	NodeName     string // 展示名
	ActorRole    string // 解析角色键（chain roles 的 key，如 supervisor）；非审批环节为空
	IsApproval   bool   // 是否生成 flow 审批任务
	BranchNote   string // 分支命中说明（R-03 上抬 / 合同二级跳过 / tier3_plus 插入）
}

// TierOf 按 chain.thresholds.purchase.bands（闭区间）判定档位 band id。
func TierOf(b *specload.Bundle, cents int64) (string, error) {
	for _, band := range b.Chain.Thresholds.Purchase.Bands {
		if band.LowerInclusive != nil && cents < *band.LowerInclusive {
			continue
		}
		if band.UpperInclusive != nil && cents > *band.UpperInclusive {
			continue
		}
		return band.ID, nil
	}
	return "", fmt.Errorf("%w: %d 分不落入任何采档区间", ErrTierNoBand, cents)
}

// RouteResult 路线判定结果。
type RouteResult struct {
	RouteID string // chain.json routes 的 key（purchase_tier2 / expense_sales / …）
	Tier    string // 采档 band id；费用线为 "expense_any"
}
