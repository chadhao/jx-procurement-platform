package httpapi

// 提交期表单校验（M4；数据源＝spec/forms/*.json 内嵌 schema，D1/N-008）。
//
// ★ 机判范围＝**结构化子集**（N-017 / R-f 已登记）：
//   - 提交时点 section（filled_at 为空）内 `source=user && required` 的字段必须提供；
//   - `is_amount_basis` 字段必须 > 0（flow 层对 AmountCents 另有兜底）；
//   - 条件必填（required_conditional）只解析最简 `==` 式（含「用途分类」中文标签映射）；
//     解析不了的条件**跳过不阻断**（自然语言类 checks 待 WorkBuddy 产出 acceptance.csv）。
//   系统字段（source=system）与审批/后置 section（filled_at 非空）不在提交时点校验。

import (
	"fmt"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// cnLabelToField 中文标签 → schema 字段名（仅机判所需的最小映射；新增须有 forms 依据）。
var cnLabelToField = map[string]string{
	"用途分类": "usage_category_l1",
}

// validateSubmitForm 对一份表单做提交期结构化校验；通过返回 nil。
// provided ＝ 提交载荷的合并视图（body.Fields + 顶层别名字段）。
func validateSubmitForm(form specload.FormDoc, provided map[string]any) error {
	for _, sec := range form.Sections {
		if strings.TrimSpace(sec.FilledAt) != "" {
			continue // 非提交时点（审批/拨付/后置/周期登记）
		}
		for _, f := range sec.Fields {
			if f.Source != "user" {
				continue // system/approver 字段不在提交时点校验
			}
			cond := strings.TrimSpace(f.RequiredConditional)
			if cond != "" {
				match, parseable := evalSimpleEqual(cond, provided)
				if !parseable || !match {
					continue
				}
			} else if !f.Required {
				continue
			}
			if !providedNonEmpty(provided, f.Name) {
				return fmt.Errorf("字段「%s」（%s）必填", f.Label, f.Name)
			}
		}
	}
	return nil
}

// providedNonEmpty 判定字段已提供（非 nil、非空串、非 0）。
func providedNonEmpty(provided map[string]any, name string) bool {
	v, ok := provided[name]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case bool:
		return true
	default:
		return true
	}
}

// evalSimpleEqual 解析最简条件 `字段 == 值`（值截断到引号/空格/全角括号）。
// 返回 (是否命中, 是否可解析)；不可解析 ⇒ 调用方跳过（不误拦）。
func evalSimpleEqual(cond string, provided map[string]any) (bool, bool) {
	i := strings.Index(cond, "==")
	if i < 0 {
		return false, false
	}
	left := strings.TrimSpace(cond[:i])
	right := strings.TrimLeft(cond[i+2:], " \t")
	// 右侧取值：带引号 ⇒ 取到闭合引号；否则截到第一个分隔符（空格/全角括号/逗号）。
	if len(right) > 0 && (right[0] == '\'' || right[0] == '"') {
		q := right[0]
		if j := strings.IndexByte(right[1:], q); j >= 0 {
			right = right[1 : 1+j]
		} else {
			return false, false
		}
	} else {
		cut := len(right)
		for _, stop := range []string{" ", "（", "(", "，", ","} {
			if j := strings.Index(right, stop); j >= 0 && j < cut {
				cut = j
			}
		}
		right = right[:cut]
	}
	if left == "" || right == "" {
		return false, false
	}
	// 左侧：中文标签映射 或 snake_case 字段名
	field, ok := cnLabelToField[left]
	if !ok {
		if isSnakeIdent(left) {
			field = left
		} else {
			return false, false // 未知左值（自然语言）⇒ 不可解析
		}
	}
	got, exists := provided[field]
	if !exists {
		return false, true // 可解析但字段未提供 ⇒ 条件不命中
	}
	return fmt.Sprintf("%v", got) == right, true
}

func isSnakeIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
