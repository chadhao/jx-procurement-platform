package permission

import (
	"sort"
	"testing"
)

// 本文件覆盖 ProjectDeep 的**递归投影**语义（B19 修复）。
//
// ★ 背景：原 Project 只裁顶层键，行内的嵌套块（台账 `formula_flags`、变更链 `archive`、
// 看板 `Supervision`）里的金额/敏感标量会**原样泄漏且不报错**。
// ★ 递归语义刻意不对称：**deny 与敏感列在任意层级生效；allow 只在顶层生效** ——
// allow 是"这一行保留哪几列"的清单，递归套用会把嵌套块结构键一起删掉、反而丢合法数据。

// TestProjectDeepDenyAndSensitiveAtAnyDepth deny 与敏感列必须穿透到任意层级。
func TestProjectDeepDenyAndSensitiveAtAnyDepth(t *testing.T) {
	row := map[string]any{
		"biz_no": "CT-2609-0001",
		"formula_flags": map[string]any{
			"same_person":        false,
			"supplier_month_sum": map[string]any{"sum_cents": 130000, "over_threshold": true},
			"amount_display":     "1,300.00",
		},
		"archive": map[string]any{
			"contract_no":    "CT-2609-0001",
			"change_cents":   100000,
			"change_display": "1,000.00",
		},
		"items": []any{
			map[string]any{"biz_no": "CH-1", "change_cents": 500},
		},
	}
	deny := []string{"amount_cents", "amount_display", "formula_flags"}
	sensitive := map[string]bool{"sum_cents": true, "change_cents": true}

	got := ProjectDeep(row, nil, deny, sensitive)

	if _, ok := got["formula_flags"]; ok {
		t.Fatalf("顶层 deny 未生效：formula_flags 仍在")
	}
	arc, ok := got["archive"].(map[string]any)
	if !ok {
		t.Fatalf("archive 丢失或类型改变: %T", got["archive"])
	}
	if _, ok := arc["change_cents"]; ok {
		t.Errorf("嵌套敏感键 change_cents 未裁掉（递归敏感列失效）")
	}
	if arc["contract_no"] != "CT-2609-0001" {
		t.Errorf("非敏感嵌套键被误删: %v", arc["contract_no"])
	}
	items := got["items"].([]any)
	it0 := items[0].(map[string]any)
	if _, ok := it0["change_cents"]; ok {
		t.Errorf("列表内元素的敏感键未裁掉（递归未覆盖 []any）")
	}
	if it0["biz_no"] != "CH-1" {
		t.Errorf("列表内非敏感键被误删: %v", it0["biz_no"])
	}
}

// TestProjectDeepAllowOnlyTopLevel allow 白名单只在顶层生效，不得删掉嵌套块的结构键。
func TestProjectDeepAllowOnlyTopLevel(t *testing.T) {
	row := map[string]any{
		"biz_no": "L07",
		"ops":    map[string]any{"经办状态": "已完成"},
		"extra":  "drop-me",
	}
	got := ProjectDeep(row, []string{"biz_no", "ops"}, nil, nil)

	if _, ok := got["extra"]; ok {
		t.Errorf("顶层 allow 未生效：extra 仍在")
	}
	ops, ok := got["ops"].(map[string]any)
	if !ok {
		t.Fatalf("ops 类型异常: %T", got["ops"])
	}
	// ★ 关键：结构键「经办状态」不在 allow 里，但它是嵌套块的内容，**必须保留**。
	if ops["经办状态"] != "已完成" {
		t.Errorf("allow 被错误地递归套用，嵌套内容被删: %v", ops)
	}
}

// TestProjectDeepSupervisionScalars 看板 Supervision 的**标量键**也必须走投影（B19 原始症状）。
func TestProjectDeepSupervisionScalars(t *testing.T) {
	sup := map[string]any{
		"requester_as_handler_count": 0,
		"concentration_max_count":    3,
		"handler_total":              12,
		"split_threshold_cents":      100000,
		"handler_concentration": []map[string]any{
			{"handler": "ou_a", "count": 3, "amount_cents": 500},
		},
	}
	got := ProjectDeep(sup, nil, []string{"amount_cents"}, map[string]bool{"split_threshold_cents": true})

	if _, ok := got["split_threshold_cents"]; ok {
		t.Errorf("监督块内的敏感标量未裁掉")
	}
	if got["handler_total"] != 12 {
		t.Errorf("非敏感标量被误删: %v", got["handler_total"])
	}
	list, ok := got["handler_concentration"].([]any)
	if !ok {
		t.Fatalf("[]map[string]any 未规范化为 []any: %T", got["handler_concentration"])
	}
	if _, ok := list[0].(map[string]any)["amount_cents"]; ok {
		t.Errorf("列表元素内的金额未裁掉")
	}
	if list[0].(map[string]any)["handler"] != "ou_a" {
		t.Errorf("列表元素内的非敏感键被误删")
	}
}

// TestProjectDeepFlatRowHardcoded 扁平行的行为用**硬编码期望**固化（P2-1）。
//
// ★ 为什么不能拿 `Project` 当基准：`Project` 现已委托给 `ProjectDeep`（同一实现），
//
//	原先「两者输出一致」的写法是**同义反复**——无论实现怎么坏都恒成立，等于没测。
//	这里改为逐例写出期望的**键集合**，任何行为变化都会真的失败。
func TestProjectDeepFlatRowHardcoded(t *testing.T) {
	row := map[string]any{"a": 1, "b": 2, "c": 3, "amount_cents": 4}

	cases := []struct {
		name        string
		allow, deny []string
		sensitive   map[string]bool
		wantKeys    []string
	}{
		{"无规则：全留", nil, nil, nil, []string{"a", "b", "c", "amount_cents"}},
		{"deny b：去掉 b", nil, []string{"b"}, nil, []string{"a", "c", "amount_cents"}},
		{"allow a,b：只留 a,b", []string{"a", "b"}, nil, nil, []string{"a", "b"}},
		{"allow 含 amount_cents + 敏感：留 a 与 amount_cents",
			[]string{"a", "amount_cents"}, nil, map[string]bool{"amount_cents": true},
			[]string{"a", "amount_cents"}},
		{"敏感但未 allow：amount_cents 消失；同时触发金额同义键规则",
			nil, nil, map[string]bool{"amount_cents": true},
			[]string{"a", "b", "c"}},
		{"deny amount_cents：金额类键一并消失",
			nil, []string{"amount_cents"}, nil,
			[]string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProjectDeep(row, tc.allow, tc.deny, tc.sensitive)
			if len(got) != len(tc.wantKeys) {
				t.Fatalf("键数 = %d（%v），期望 %d（%v）", len(got), keysOf(got), len(tc.wantKeys), tc.wantKeys)
			}
			for _, k := range tc.wantKeys {
				if _, ok := got[k]; !ok {
					t.Fatalf("缺少键 %s（实际 %v）", k, keysOf(got))
				}
			}
		})
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestIsAmountKey 金额类键名识别：后缀规则必须覆盖「将来新增的金额键名」。
func TestIsAmountKey(t *testing.T) {
	yes := []string{"amount_cents", "amount_display", "change_cents", "change_display",
		"original_cents", "original_display", "sum_cents", "paid_cents", "amount", "formula_flags",
		"AMOUNT_CENTS", " change_cents "}
	no := []string{"contract_no", "biz_no", "supplier", "department", "amount_note", "cents", "display"}
	for _, k := range yes {
		if !isAmountKey(k) {
			t.Errorf("isAmountKey(%q) 应为真", k)
		}
	}
	for _, k := range no {
		if isAmountKey(k) {
			t.Errorf("isAmountKey(%q) 应为假（不得把非金额键误裁）", k)
		}
	}
}
