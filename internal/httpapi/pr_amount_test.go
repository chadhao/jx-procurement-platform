package httpapi

// N-039 P0 验收：PR 定档依据服务端权威化 —— 双向探针（★ 只做"低报必拒"会误伤合法单）。
//   正向：明细合规、amount 与汇总一致 ⇒ 通过，且定档值＝服务端汇总（表头回写）；
//   反向：客户端低报 amount ⇒ fail-closed 400 语义错误（绝不静默采信）；
//   另：行小计由服务端按单价×数量算（客户端行内 subtotal 伪造无效）、缺明细 fail-closed。

import (
	"strings"
	"testing"
)

func i64p(v int64) *int64 { return &v }

// TestPRAmountServerAuthoritative 双向探针（正向＝合规单必过）。
func TestPRAmountServerAuthoritative(t *testing.T) {
	// 合规单：两行明细 10000×2 + 5000×4 = 40000 分；客户端 amount 与汇总一致
	body := &approvalSubmitBody{
		DocType:     "PR",
		AmountCents: i64p(40000),
		Fields: map[string]any{
			"detail": []any{
				map[string]any{"estimated_unit_price_cents": float64(10000), "quantity": float64(2),
					"subtotal_cents": float64(1)}, // 伪造行小计：必须被忽略
				map[string]any{"estimated_unit_price_cents": float64(5000), "quantity": float64(4)},
			},
		},
	}
	got, err := resolvePRAmountForTier(body)
	if err != nil {
		t.Fatalf("合规单应通过: %v", err)
	}
	if got == nil || *got != 40000 {
		t.Fatalf("定档值 = %v, 期望服务端汇总 40000（行小计伪造 1 分必须被忽略）", got)
	}
	if body.Fields["estimated_total_cents"] != int64(40000) {
		t.Errorf("表头 estimated_total_cents = %v, 期望服务端回写 40000", body.Fields["estimated_total_cents"])
	}
}

// TestPRAmountLowBallRejected 反向：伪造低报 ⇒ 必拒（fail-closed）。
func TestPRAmountLowBallRejected(t *testing.T) {
	// 同一明细汇总 40000；客户端低报 9999（试图降到采一档 <100000 分）
	body := &approvalSubmitBody{
		DocType:     "PR",
		AmountCents: i64p(9999),
		Fields: map[string]any{
			"detail": []any{
				map[string]any{"estimated_unit_price_cents": float64(10000), "quantity": float64(2)},
				map[string]any{"estimated_unit_price_cents": float64(5000), "quantity": float64(4)},
			},
		},
	}
	if _, err := resolvePRAmountForTier(body); err == nil {
		t.Fatal("低报 amount_cents 未被拒 —— 定档依据仍可被客户端决定（P0 未闭环）")
	} else if !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("错误应点名交叉校验不一致: %v", err)
	}
	// 且不回写表头（拒收时不产生任何权威值副作用）
	if _, has := body.Fields["estimated_total_cents"]; has {
		t.Error("拒绝路径不应回写 estimated_total_cents")
	}
}

// TestPREstimatedTotalEdges 汇总边界：缺明细 / 空数组 / 缺行字段 / 非正单价 —— 全 fail-closed。
func TestPREstimatedTotalEdges(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
	}{
		{"缺明细", map[string]any{}},
		{"空数组", map[string]any{"detail": []any{}}},
		{"行缺数量", map[string]any{"detail": []any{
			map[string]any{"estimated_unit_price_cents": float64(100)}}}},
		{"零单价", map[string]any{"detail": []any{
			map[string]any{"estimated_unit_price_cents": float64(0), "quantity": float64(1)}}}},
		{"行非对象", map[string]any{"detail": []any{"x"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := computePREstimatedTotal(c.fields); err == nil {
				t.Fatalf("%s 应 fail-closed", c.name)
			}
		})
	}
	// 小数数量：1.5 × 20000 = 30000
	sum, err := computePREstimatedTotal(map[string]any{"detail": []any{
		map[string]any{"estimated_unit_price_cents": float64(20000), "quantity": float64(1.5)},
	}})
	if err != nil || sum != 30000 {
		t.Fatalf("小数数量汇总 = %d/%v, 期望 30000", sum, err)
	}
}

// TestSubmitFormSkipsRepeatingSection N-039：PR#detail（repeating）行字段不在顶层判必填 ——
// 明细数组形态的合规单必须能过结构化校验（否则 P0 双向探针的"合规必过"走不到提交）。
func TestSubmitFormSkipsRepeatingSection(t *testing.T) {
	form := metaTestBundle(t).Forms["PR"]
	if form.DocType == "" {
		t.Fatal("缺 PR 表单")
	}
	// 顶层只给 header 必填字段；detail 行字段全在数组里（repeating 顶层不判）
	topLevel := map[string]any{
		"usage_category_l1":              "P01",
		"usage_category_l2":              "主原料",
		"requirement_type":               "常规",
		"purpose":                        "补一批滤布",
		"required_date":                  "2026-12-01",
		"urgent_level":                   "常规",
		"budget_subject":                 "生产预算",
		"is_safety_or_special_equipment": false,
		"is_fixed_asset":                 false,
		"estimated_total_cents":          float64(40000),
		"detail": []any{
			map[string]any{"material_name_spec": "钢板 3mm", "unit": "个",
				"quantity": float64(2), "estimated_unit_price_cents": float64(10000)},
		},
	}
	if err := validateSubmitForm(form, topLevel); err != nil {
		t.Fatalf("明细数组形态的合规 PR 应过结构化校验（repeating 顶层不判）: %v", err)
	}
}
