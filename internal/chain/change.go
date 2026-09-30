package chain

// 采购变更档位（R-15 · R-30）—— 公式与不变量锚定 spec/chain.json#routes.change.rules。
//
//	R-15：tier = tier_of(max(change_amount_cents, original_contract_amount_cents))（档位就高）
//	R-30：**采一档分支不可达** —— 合同前提保证原合同 ≥1,000 元 ⇒ max(...) ≥1,000
//	      ⇒ 任何合法 PC 单都不会落入 purchase_tier1（不变量，见属性测试）。

import (
	"fmt"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// ChangeTierOf 采购变更的适用档位（R-15 就高公式：max(change, original)）。
func ChangeTierOf(b *specload.Bundle, changeCents, originalCents int64) (string, error) {
	base := changeCents
	if originalCents > base {
		base = originalCents // 含「变更额为负（减项）」情形：就高取原合同额
	}
	tier, err := TierOf(b, base)
	if err != nil {
		return "", fmt.Errorf("chain: 变更档位计算失败（max(%d,%d)）: %w", changeCents, originalCents, err)
	}
	return tier, nil
}
