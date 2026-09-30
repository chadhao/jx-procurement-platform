package chain

// 付款路径（T3 / R-26 用户 B2 定案）——权威源 spec/chain.json#payment_route_rule。
//
// ★ 判定轴＝**是否签合同**，不是金额（金额只决定走哪条审批链，不再单独决定付款路径）。
// ★ 4 条优先级（实现按 decisions[].priority 消费，payment_route **值从 spec 读**，不硬编码）：
//	 1. has_contract == true                        → group_public_account（不论金额/档位/单据类型）
//	 2. 无合同 && 分类判为「对公直付」               → group_public_account（复用 enums#route_resolution 口径）
//	 3. 无合同 && tier == purchase_tier1            → petty_cash（制度第十八条）
//	 4. otherwise                                   → personal_advance_reimburse
// ★ 边界（boundary_decided_by_us）：<1,000 元但签了合同 ⇒ 公户付款 **且** 合同两级照走 ——
//   前者由本函数 priority 1 覆盖，后者由 BuildNodes 的 R-26 插入覆盖。
// ★ 键名纪律（R-26 教训）：本结果叫 payment_route；`route` 已被占用为「审批流程线」。

import (
	"fmt"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// 付款路径值域（从 spec decisions 读出前的键名常量 —— 仅作类型提示；
// 实际返回值一律经 routeValue() 从 chain.json 取）。
const (
	payRouteGroupPublic = "group_public_account"
	payRoutePettyCash   = "petty_cash"
	payRoutePersonalAdv = "personal_advance_reimburse"
)

// PaymentRouteOf 计算付款路径（纯函数；输入 Facts + 内嵌 spec）。
func PaymentRouteOf(b *specload.Bundle, f Facts) (string, error) {
	// priority 1：有合同 ⇒ 公户（不论金额/档位/单据类型）
	if f.HasContract {
		return routeValue(b, 1)
	}
	// priority 2：无合同 && 分类判为对公直付（enums#route_resolution 口径）
	if isDirectPaidClassification(f) {
		return routeValue(b, 2)
	}
	// priority 3：无合同 && 采一档（备付金直接支出）
	if isPurchaseTier1(b, f) {
		return routeValue(b, 3)
	}
	// priority 4：其余 ⇒ 个人垫付 + 报销
	return routeValue(b, 4)
}

// routeValue 从 payment_route_rule.decisions 按 priority 取 payment_route 值
// （**真消费 spec** —— 改 spec 中的值，行为随之变化）。
func routeValue(b *specload.Bundle, priority int) (string, error) {
	for _, d := range b.Chain.PaymentRouteRule.Decisions {
		if d.Priority == priority {
			if d.PaymentRoute == "" {
				return "", fmt.Errorf("chain: payment_route_rule(priority=%d) 的 payment_route 为空", priority)
			}
			return d.PaymentRoute, nil
		}
	}
	return "", fmt.Errorf("chain: payment_route_rule 缺 priority=%d 的决策（spec 不完整）", priority)
}

// isDirectPaidClassification 是否「对公直付」类（priority 2 判据）——
// 复用 enums#route_resolution 既有口径（**同一件事不在两处定义**）：
//   - P08 / M07 ⇒ 恒对公直付；
//   - M01/M02/M03/M06 ⇒ 按支付方式二分（corporate_direct ⇒ 直付）；
//   - 其余（含 S 线与 P01–P07 采购类）⇒ false。
func isDirectPaidClassification(f Facts) bool {
	l1 := strings.ToUpper(strings.TrimSpace(f.UsageCategoryL1))
	switch l1 {
	case "P08", "M07":
		return true
	case "M01", "M02", "M03", "M06":
		return normalizePayment(f.PaymentMethodInput) == "corporate_direct"
	default:
		return false
	}
}

// isPurchaseTier1 该事实是否为**采购线**的采一档（priority 3 判据）。
// ★ 只对采购线单据（BA/PR/…）取采档：费用线单据（SA 等）没有「采一档」语义 ——
//
//	500 元的费用事前申请也**不走备付金**（备付金＝采一档采购专用，制度第十八条）。
func isPurchaseTier1(b *specload.Bundle, f Facts) bool {
	if f.DocType == DocSA {
		return false // 费用线（批 1 范围内唯一的费用单据）
	}
	if f.AmountCents == nil {
		return false
	}
	tier, err := TierOf(b, *f.AmountCents)
	if err != nil {
		return false
	}
	return tier == "purchase_tier1"
}

func normalizeCategory(s string) string {
	out := []rune{}
	for _, r := range s {
		if r != ' ' && r != '\t' {
			out = append(out, r)
		}
	}
	return string(out)
}
