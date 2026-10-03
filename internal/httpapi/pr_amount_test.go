package httpapi

// N-039 P0 ＋ N-040 裁定验收：
//   正向＝前端真实形状（**不带顶层 amount_cents**）合规必过；
//   裁定③＝传了且不一致 ⇒ **不 400**、mismatch 信号（handler 审计 warn）、定档用服务端值；
//   裁定④＝服务端汇总口径不变（行小计＝单价×数量、忽略客户端行 subtotal、缺明细 fail-closed）；
//   行级必填＝repeating 段 fields.<id> 数组逐行校验（N-040 裁定③服务端半）。

import (
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func i64p(v int64) *int64 { return &v }

// TestPRAmountFrontendShapePasses 契约正向：前端真实载荷形状（无顶层 amount_cents）
// 驱动 resolve ⇒ 合规必过、定档值＝服务端汇总、表头回写。
func TestPRAmountFrontendShapePasses(t *testing.T) {
	body := &approvalSubmitBody{
		DocType: "PR", // AmountCents 缺省 = nil（Submit.vue#doSubmit 不发）
		Fields: map[string]any{
			"detail": []any{
				map[string]any{"estimated_unit_price_cents": float64(10000), "quantity": float64(2),
					"subtotal_cents": float64(1)}, // 行小计伪造：服务端忽略
				map[string]any{"estimated_unit_price_cents": float64(5000), "quantity": float64(4)},
			},
		},
	}
	got, mismatch, err := resolvePRAmountForTier(body)
	if err != nil {
		t.Fatalf("前端真实形状（缺 amount_cents）应通过（N-040 裁定②）: %v", err)
	}
	if mismatch {
		t.Error("客户端未传 amount_cents 不构成 mismatch（只在传了且不一致时才 warn）")
	}
	if got == nil || *got != 40000 {
		t.Fatalf("定档值 = %v, 期望服务端汇总 40000（行小计伪造必须被忽略）", got)
	}
	if body.Fields["estimated_total_cents"] != int64(40000) {
		t.Errorf("表头回写 = %v, 期望 40000", body.Fields["estimated_total_cents"])
	}
}

// TestPRAmountMismatchWarnNotReject N-040 裁定③：传了且不一致 ⇒ 不报错、
// 返回 mismatch（handler 落 amount_vs_server_sum_mismatch 审计 warn）、仍用服务端值。
func TestPRAmountMismatchWarnNotReject(t *testing.T) {
	body := &approvalSubmitBody{
		DocType:     "PR",
		AmountCents: i64p(9999), // 低报
		Fields: map[string]any{
			"detail": []any{
				map[string]any{"estimated_unit_price_cents": float64(10000), "quantity": float64(2)},
				map[string]any{"estimated_unit_price_cents": float64(5000), "quantity": float64(4)},
			},
		},
	}
	got, mismatch, err := resolvePRAmountForTier(body)
	if err != nil {
		t.Fatalf("裁定③：不一致不许 400（服务端值已是权威，阻塞只增误伤）: %v", err)
	}
	if !mismatch {
		t.Fatal("不一致必须返回 mismatch 信号（handler 据此落审计 warn）")
	}
	if got == nil || *got != 40000 {
		t.Fatalf("定档值 = %v, 期望服务端汇总 40000（不采信低报 9999）", got)
	}
}

// TestPRAmountMatchNoMismatch 传了且一致 ⇒ 无信号（对照组）。
func TestPRAmountMatchNoMismatch(t *testing.T) {
	body := &approvalSubmitBody{
		DocType: "PR", AmountCents: i64p(20000),
		Fields: map[string]any{
			"detail": []any{
				map[string]any{"estimated_unit_price_cents": float64(10000), "quantity": float64(2)},
			},
		},
	}
	_, mismatch, err := resolvePRAmountForTier(body)
	if err != nil || mismatch {
		t.Fatalf("一致时应过且无 mismatch: err=%v mismatch=%v", err, mismatch)
	}
}

// TestPREstimatedTotalEdges 裁定④：服务端汇总口径不变 —— 缺明细/空/缺行字段/非正单价全 fail-closed。
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
				t.Fatalf("%s 应 fail-closed（裁定④保留）", c.name)
			}
		})
	}
	sum, err := computePREstimatedTotal(map[string]any{"detail": []any{
		map[string]any{"estimated_unit_price_cents": float64(20000), "quantity": float64(1.5)},
	}})
	if err != nil || sum != 30000 {
		t.Fatalf("小数数量汇总 = %d/%v, 期望 30000", sum, err)
	}
}

// ---- N-040 裁定③服务端半：repeating 行级必填 ----

// TestRepeatingRowLevelRequired 行级必填：行内 required 缺 ⇒ 400 语义错误；
// 行齐（含 optional 缺省）⇒ 过；行缺失/空数组 ⇒ 报（有 required 的组无处承载）。
func TestRepeatingRowLevelRequired(t *testing.T) {
	form := metaTestBundle(t).Forms["PR"]
	var detailSec specload.SectionDoc
	for _, s := range form.Sections {
		if s.Repeating {
			detailSec = s
		}
	}
	if detailSec.ID == "" {
		t.Fatal("PR 表单缺 repeating section（detail）—— spec 结构变了？")
	}

	full := map[string]any{"detail": []any{
		map[string]any{"material_name_spec": "钢板", "unit": "个",
			"quantity": float64(1), "estimated_unit_price_cents": float64(10000)},
	}}
	if err := validateRepeatingRows(detailSec, full); err != nil {
		t.Fatalf("行字段齐应过: %v", err)
	}

	missing := map[string]any{"detail": []any{
		map[string]any{"unit": "个", "quantity": float64(1), "estimated_unit_price_cents": float64(10000)},
	}}
	err := validateRepeatingRows(detailSec, missing)
	if err == nil {
		t.Fatal("行缺 material_name_spec 应 400 可见失败")
	}
	if !strings.Contains(err.Error(), "第 1 行") {
		t.Fatalf("错误应点名行号: %v", err)
	}

	if err := validateRepeatingRows(detailSec, map[string]any{}); err == nil {
		t.Fatal("缺 detail 数组应报（有 required 行字段无处承载）")
	}
	if err := validateRepeatingRows(detailSec, map[string]any{"detail": []any{}}); err == nil {
		t.Fatal("空数组应报（至少 1 行）")
	}
}

// TestSubmitFormRepeatingContract N-040 契约：validateSubmitForm 对 repeating 走行级校验 ——
// 顶层不判（行字段不在顶层）但行内缺必填 ⇒ 400。
func TestSubmitFormRepeatingContract(t *testing.T) {
	form := metaTestBundle(t).Forms["PR"]
	topLevel := func(detail any) map[string]any {
		m := map[string]any{
			"usage_category_l1": "P01", "usage_category_l2": "主原料",
			"requirement_type": "常规", "purpose": "补一批滤布",
			"required_date": "2026-12-01", "urgent_level": "常规",
			"budget_subject":                 "生产预算",
			"is_safety_or_special_equipment": false, "is_fixed_asset": false,
		}
		if detail != nil {
			m["detail"] = detail
		}
		return m
	}
	// ① 合规（行字段齐）⇒ 过
	ok := []any{map[string]any{"material_name_spec": "钢板", "unit": "个",
		"quantity": float64(1), "estimated_unit_price_cents": float64(10000)}}
	if err := validateSubmitForm(form, topLevel(ok)); err != nil {
		t.Fatalf("合规明细应过: %v", err)
	}
	// ② 行缺必填 ⇒ 400（可见失败）
	bad := []any{map[string]any{"unit": "个", "quantity": float64(1),
		"estimated_unit_price_cents": float64(10000)}}
	err := validateSubmitForm(form, topLevel(bad))
	if err == nil {
		t.Fatal("行内缺 material_name_spec 应由行级校验拦下")
	}
	if !strings.Contains(err.Error(), "第 1 行") {
		t.Fatalf("错误应点名行: %v", err)
	}
}
