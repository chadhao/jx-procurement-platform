package httpapi

// N-039 P0 · PR 定档依据服务端权威化 ＋ N-040 裁定①②④（口径调整）。
//
// ★ spec/forms/PR.json：`estimated_total_cents`（computed · immutable · **is_amount_basis**）
// rule＝「公式汇总明细小计，禁止手填」· critical_note＝「该值是**分档判定的唯一依据**」。
//
// ★★ N-040 裁定后的口径（以裁定为准）：
//   ① 定档（chain.Facts）与落库（flow.Submit）**一律取服务端按明细汇总值**；
//   ② 客户端顶层 `amount_cents` **不再要求** —— 缺失＝正常，不报错
//     （真实客户端 Submit.vue 本就不发该字段 —— N-040 查出的覆盖盲区）；
//   ③ 客户端**若**传了且与汇总不一致 ⇒ **不 400**：返回 mismatch 信号，由 handler 落
//     审计 warn（action=amount_vs_server_sum_mismatch）＋ **仍以服务端值定档落库**
//     （服务端值已是权威，阻塞只增误伤；不一致本身是要可见的信号）；
//   ④ 服务端汇总口径**不变**：行小计＝round(单价×数量)、忽略客户端行内 subtotal_cents、
//     缺明细/结构非法 ⇒ fail-closed（没有定档依据就没有档位）。

import (
	"fmt"
	"math"
)

// resolvePRAmountForTier N-039/N-040 入口：服务端汇总 → 回写表头 estimated_total_cents
// （服务端权威恒覆盖）→ 返回定档/落库用的服务端值。
//   - error：仅服务端算不出汇总时（裁定④ fail-closed 面）；
//   - mismatch：客户端传了顶层 amount_cents 且与汇总不一致（裁定③ —— 不是错误，
//     handler 据此落审计 warn，定档落库照走服务端值）。
func resolvePRAmountForTier(body *approvalSubmitBody) (estimated *int64, mismatch bool, err error) {
	sum, cerr := computePREstimatedTotal(body.Fields)
	if cerr != nil {
		return nil, false, cerr
	}
	if body.AmountCents != nil && *body.AmountCents != sum {
		mismatch = true // 裁定③：可见信号，不拒单
	}
	if body.Fields == nil {
		body.Fields = map[string]any{}
	}
	body.Fields["estimated_total_cents"] = sum
	return &sum, mismatch, nil
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
