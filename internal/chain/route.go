package chain

import (
	"fmt"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// ResolveRoute 由 Facts 判定流程线（tier + route）。
//
// 判定依据（全部来自 spec/，不自造口径）：
//   - BA：恒采一档线（chain.json doc_chains.BA.route = purchase_tier1）；
//     金额 >99999 ⇒ ErrTierOutOfDoc（N-012 过渡：forms/BA.checks.amount_tier1_only）。
//   - PR：doc_chains.PR.route_by_tier（仅 tier2/tier3）；金额 <1000 ⇒ ErrTierOutOfDoc
//     引导走 BA（N-012 过渡口径，待 WorkBuddy 裁定）。
//   - SA：spec/enums.json#route_resolution（按用途分类组 + 支付方式三线）。
func ResolveRoute(b *specload.Bundle, f Facts) (RouteResult, error) {
	switch f.DocType {
	case DocBA:
		if f.AmountCents == nil {
			return RouteResult{}, ErrAmountMissing
		}
		tier, err := TierOf(b, *f.AmountCents)
		if err != nil {
			return RouteResult{}, err
		}
		if tier != "purchase_tier1" {
			return RouteResult{}, fmt.Errorf(
				"%w: 采购报备单仅适用采一档（< 1,000 元），当前 %d 分落 %s，请改走物资采购申请单（PR）",
				ErrTierOutOfDoc, *f.AmountCents, tier)
		}
		return RouteResult{RouteID: "purchase_tier1", Tier: tier}, nil

	case DocPR:
		if f.AmountCents == nil {
			return RouteResult{}, ErrAmountMissing
		}
		tier, err := TierOf(b, *f.AmountCents)
		if err != nil {
			return RouteResult{}, err
		}
		if tier == "purchase_tier1" {
			return RouteResult{}, fmt.Errorf(
				"%w: 物资采购申请单无采一链（< 1,000 元），请改走采购报备单（BA）",
				ErrTierOutOfDoc)
		}
		dc := b.Chain.DocChains[DocPR]
		route, ok := dc.RouteByTier[tier]
		if !ok {
			return RouteResult{}, fmt.Errorf("%w: PR route_by_tier 缺 %s", ErrRouteMissing, tier)
		}
		return RouteResult{RouteID: route, Tier: tier}, nil

	case DocSA:
		route, err := resolveExpenseRoute(f.UsageCategoryL1, f.PaymentMethodInput)
		if err != nil {
			return RouteResult{}, err
		}
		return RouteResult{RouteID: route, Tier: "expense_any"}, nil

	case DocCT:
		// 合同/简式订单：**恒走合同统一两级**（doc_chains.CT.route = contract_two_level，
		// S6 extra_allowed 已认可该引用）；触发条件＝是否签合同（R-26）—— CT 即有合同。
		tier := ""
		if f.AmountCents != nil {
			if t, err := TierOf(b, *f.AmountCents); err == nil {
				tier = t
			}
		}
		return RouteResult{RouteID: b.Chain.ContractApproval.ID, Tier: tier}, nil

	default:
		return RouteResult{}, fmt.Errorf("%w: %s", ErrUnsupportedDoc, f.DocType)
	}
}

// resolveExpenseRoute 按 spec/enums.json#route_resolution 归线：
//   - P08 / M07 ⇒ 对公直付线（只能直付，不可提前支出）
//   - M01 / M02 / M03 / M06 ⇒ 按支付方式二分（直付 / 垫付）
//   - S01–S07 / M04 / M05 / M08 ⇒ 运营销售费用线
//   - P01–P07 ⇒ 属采购线，不应出现在费用单（SA）上
//
// ★ conflict_note（enums 原文）：P08/M07「只能对公直付」、M01/02/03/06「可垫付可直付」，
//
//	两组口径不同，实现时勿合并。
func resolveExpenseRoute(usageL1, payment string) (string, error) {
	l1 := strings.ToUpper(strings.TrimSpace(usageL1))
	switch {
	case l1 == "P08" || l1 == "M07":
		return "expense_mgmt_direct", nil
	case l1 == "M01" || l1 == "M02" || l1 == "M03" || l1 == "M06":
		// ★ 双语归一（spec 自身双写）：enums.json#payment_method_input.values 是中文
		//   （用户输入权威），route_resolution 规则串是英文键 —— 两者皆 spec 原文，均接受。
		switch normalizePayment(payment) {
		case "corporate_direct":
			return "expense_mgmt_direct", nil
		case "personal_advance":
			return "expense_mgmt_advance", nil
		default:
			return "", fmt.Errorf("%w: M 类可逆向组须 payment_method_input ∈ {personal_advance/个人垫付, corporate_direct/对公直付}，实为 %q",
				ErrPaymentInvalid, payment)
		}
	case strings.HasPrefix(l1, "S"), l1 == "M04", l1 == "M05", l1 == "M08":
		return "expense_sales", nil
	case strings.HasPrefix(l1, "P"):
		return "", fmt.Errorf("%w: %s 属采购线（P01–P07），不应出现在费用事前申请单上",
			ErrCategoryInvalid, l1)
	default:
		return "", fmt.Errorf("%w: 未知用途分类 %q", ErrCategoryInvalid, usageL1)
	}
}

// normalizePayment 支付方式双语归一（见 resolveExpenseRoute 注释）。
func normalizePayment(p string) string {
	switch strings.TrimSpace(p) {
	case "个人垫付", "垫付":
		return "personal_advance"
	case "对公直付":
		return "corporate_direct"
	default:
		return strings.TrimSpace(p)
	}
}
