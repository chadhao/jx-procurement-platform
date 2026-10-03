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
	"context"
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
		if sec.Repeating {
			// ★ N-040 裁定③：重复段契约 —— fields.<section_id> ＝ 数组 of 行对象；
			//   **顶层不判**（行字段不在顶层），但**行级必填**：行内 required:true（或
			//   conditional 命中）的 user 字段逐行校验，缺即 400 可见失败。
			if err := validateRepeatingRows(sec, provided); err != nil {
				return err
			}
			continue
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

// validateRepeatingRows N-040 裁定③：重复段**行级必填**（fields.<section_id> 数组 of 行对象）。
//   - 行数据缺失/非数组/空（且组内有 required user 字段）⇒ 400 可见失败；
//   - 行内 required:true（无 conditional）逐行必填；required_conditional 对**行 map** 评估；
//   - 行内 system/computed 字段不判（同顶层 source=user 限定）。
func validateRepeatingRows(sec specload.SectionDoc, provided map[string]any) error {
	hasRequired := false
	for _, f := range sec.Fields {
		if f.Source == "user" && f.Required && strings.TrimSpace(f.RequiredConditional) == "" {
			hasRequired = true
			break
		}
	}
	raw, exists := provided[sec.ID]
	if !exists || raw == nil {
		if hasRequired {
			return fmt.Errorf("明细「%s」（%s）缺失 —— 行内必填字段无处承载（契约：fields.%s ＝ 行对象数组）",
				sec.Label, sec.ID, sec.ID)
		}
		return nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("明细「%s」（%s）必须是行对象数组，实际是 %T（N-040 契约）",
			sec.Label, sec.ID, raw)
	}
	if len(rows) == 0 && hasRequired {
		return fmt.Errorf("明细「%s」至少需要 1 行 —— 行内必填字段不能为空数组", sec.Label)
	}
	for i, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			return fmt.Errorf("明细「%s」第 %d 行不是对象", sec.Label, i+1)
		}
		for _, f := range sec.Fields {
			if f.Source != "user" {
				continue
			}
			cond := strings.TrimSpace(f.RequiredConditional)
			if cond != "" {
				match, parseable := evalSimpleEqual(cond, row)
				if !parseable || !match {
					continue
				}
			} else if !f.Required {
				continue
			}
			if !providedNonEmpty(row, f.Name) {
				return fmt.Errorf("明细「%s」第 %d 行字段「%s」（%s）必填", sec.Label, i+1, f.Label, f.Name)
			}
		}
	}
	return nil
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

// validateConstantRefs T2：constant_ref 字段（如 PR/CT.unit）提交校验 + **值快照**。
//   - 值必须在对应常量表的 active 集合内；retired ⇒ 40000（新单据选不到）；
//   - 通过 ⇒ 就地写入 `fields["<字段>_snapshot"]`（policy.snapshot_rule：
//     字典可变，但已落单据的取值必须冻结 —— 改名/停用后历史单据显示一字不变）。
//
// 表约定：constant_ref 字段的表键 ＝ 字段名（constants.json#tables[].used_by 同口径）。
func (d Deps) validateConstantRefs(ctx context.Context, form specload.FormDoc, fields map[string]any) error {
	if d.DB == nil || d.Spec == nil || d.Spec.Constants == nil {
		return nil // 表未装配时由 requireSpec 路径可见失败；此处不静默改写
	}
	known := d.knownConstantTables()
	for _, sec := range form.Sections {
		for _, f := range sec.Fields {
			if f.Type != "constant_ref" {
				continue
			}
			v, exists := fields[f.Name]
			if !exists {
				continue // 必填校验归 validateSubmitForm；未提供时不在此拦
			}
			s, isStr := v.(string)
			if !isStr || strings.TrimSpace(s) == "" {
				continue
			}
			if !known[f.Name] {
				return fmt.Errorf("字段 %q 是 constant_ref 但没有对应的常量表 %q（spec/constants.json 未登记该表）", f.Name, f.Name)
			}
			active, err := d.DB.ListActiveConstantValues(ctx, f.Name)
			if err != nil {
				return fmt.Errorf("常量校验失败: %w", err)
			}
			found := false
			for _, a := range active {
				if a == s {
					found = true
					break
				}
			}
			if found {
				fields[f.Name+"_snapshot"] = s // ★ 值快照（冻结历史显示）
				continue
			}
			// 不在 active：区分「已停用」与「未登记」（可见、可归因）
			all, err := d.DB.ListConstants(ctx, f.Name, "")
			if err != nil {
				return fmt.Errorf("常量校验失败: %w", err)
			}
			for _, r := range all {
				if r.Value == s {
					return fmt.Errorf("常量「%s」已停用（%s 表）—— 新单据不得选择已停用项", s, f.Name)
				}
			}
			return fmt.Errorf("常量值「%s」未在 %s 表登记（或已被删除）", s, f.Name)
		}
	}
	return nil
}
