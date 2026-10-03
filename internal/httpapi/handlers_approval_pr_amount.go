package httpapi

// N-039 P0 · PR 定档依据服务端权威化。
//
// ★ spec/forms/PR.json：`estimated_total_cents`（computed · immutable · **is_amount_basis**）
// rule＝「公式汇总明细小计，禁止手填」· critical_note＝「该值是**分档判定的唯一依据**」。
// ★ 口径（rule 原文）：行 `subtotal_cents` ＝ 单价 × 数量（**服务端算，不吃客户端行小计**），
// 表头 `estimated_total_cents` ＝ Σ行小计；客户端顶层 `amount_cents` 与之**不一致 ⇒ 400
// （fail-closed）**，定档（chain.Facts.AmountCents）与落库（flow.Submit）一律用服务端值。
// ★ 低报降档路径由本函数闭环：伪造低报 ⇒ 与汇总不一致 ⇒ 可见拒绝，绝不静默采信。

import (
	"fmt"
	"math"
)

// resolvePRAmountForTier N-039 P0 入口：服务端汇总 → 与客户端 amount_cents 交叉校验
// （不一致 ⇒ fail-closed）→ 回写表头 estimated_total_cents（服务端权威恒覆盖）。
// 返回供定档（chain.Facts）与落库（flow.Submit）使用的**服务端汇总值**。
func resolvePRAmountForTier(body *approvalSubmitBody) (*int64, error) {
	estimated, err := computePREstimatedTotal(body.Fields)
	if err != nil {
		return nil, err
	}
	if body.AmountCents == nil || *body.AmountCents != estimated {
		got := "null"
		if body.AmountCents != nil {
			got = fmt.Sprintf("%d", *body.AmountCents)
		}
		return nil, fmt.Errorf(
			"amount_cents 与服务端按明细汇总的 estimated_total_cents 不一致（客户端=%s 服务端=%d）—— 定档依据唯一＝服务端汇总，fail-closed",
			got, estimated)
	}
	if body.Fields == nil {
		body.Fields = map[string]any{}
	}
	body.Fields["estimated_total_cents"] = estimated
	return &estimated, nil
}

// computePREstimatedTotal 按明细行（fields["detail"] 数组）服务端汇总 PR 定档依据。
//   - 行小计 ＝ round(estimated_unit_price_cents × quantity)（单价为分；数量可小数）；
//   - 客户端行内 subtotal_cents **一律忽略**（computed 禁手填 —— 只信单价×数量）；
//   - 无明细 / 行结构非法 ⇒ 错误（fail-closed：没有定档依据就没有档位）。
func computePREstimatedTotal(fields map[string]any) (int64, error) {
	raw, ok := fields["detail"]
	if !ok {
		return 0, fmt.Errorf("PR 明细（fields.detail）缺失 —— 定档依据必须来自服务端按明细汇总（rule：公式汇总明细小计）")
	}
	rows, ok := raw.([]any)
	if !ok || len(rows) == 0 {
		return 0, fmt.Errorf("PR 明细（fields.detail）必须是非空数组 —— 无明细即无定档依据")
	}
	var sum int64
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("PR 明细第 %d 行不是对象", i+1)
		}
		price, pok := toFloat64(m["estimated_unit_price_cents"])
		qty, qok := toFloat64(m["quantity"])
		if !pok || !qok {
			return 0, fmt.Errorf("PR 明细第 %d 行缺单价（estimated_unit_price_cents）或数量（quantity）—— 行小计无法服务端计算", i+1)
		}
		if price <= 0 || qty <= 0 {
			return 0, fmt.Errorf("PR 明细第 %d 行单价/数量必须为正（单价=%v 数量=%v）", i+1, price, qty)
		}
		sum += int64(math.Round(price * qty))
	}
	if sum <= 0 {
		return 0, fmt.Errorf("PR 明细汇总为 %d —— 定档依据必须为正", sum)
	}
	return sum, nil
}
