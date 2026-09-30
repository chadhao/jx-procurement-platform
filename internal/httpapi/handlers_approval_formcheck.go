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

// evaluateHardChecks T4：severity=hard 的结构化判据机判（数据源＝spec/forms/*.json#checks）。
//
// ★ 本批覆盖两条（两处必须同口径，R-27/B5）：
//  1. `amount_vs_pr`（CT）：合同额 ≤ PR 预估额 ×(1+容差%)；容差读
//     `params.json#contract.amount_over_pr_tolerance_percent`（**不写死 10**）、
//     超容差动作读 `contract.over_tolerance_action`（require_purchase_change ⇒
//     提示先走采购变更 PC）。关联 PR 经 `related_biz_no` 实查 —— 查不到可见失败，
//     **绝不 fail-open**（fail-open＝假校验）。
//  2. `no_self_purchaser`（PR/CT 同款）：**禁止自批自派自经办** ——
//     拦截条件＝`designated_by == 需求提出人`（R-27；验收口径：
//     指定人＝提出人 ⇒ 拦；指定人≠提出人 ⇒ 放行；上级指派时经办人可以是提出人）。
//     designated_by 未提供 ⇒ 跳过（该字段在审批时点填报；提交时点通常缺省）。
func (d Deps) evaluateHardChecks(ctx context.Context, form specload.FormDoc, body *approvalSubmitBody, applicantOpenID string) error {
	severityOf := func(id string) string {
		for _, c := range form.Checks {
			if c.ID == id {
				return c.Severity
			}
		}
		return ""
	}

	// ---- amount_vs_pr（hard）----
	if severityOf("amount_vs_pr") == "hard" {
		related, _ := body.Fields["related_biz_no"].(string)
		related = strings.TrimSpace(related)
		if related == "" {
			return fmt.Errorf("硬判据 amount_vs_pr 需要 related_biz_no（关联 PR 单号）才能校验合同金额容差")
		}
		pr, err := d.DB.GetInstanceByBizNo(ctx, related)
		if err != nil || pr.AmountCents == nil || *pr.AmountCents <= 0 {
			return fmt.Errorf("硬判据 amount_vs_pr 无法关联 PR %q（不存在或无预估金额）—— 不放行（fail-open＝假校验）", related)
		}
		contractAmt := body.AmountCents
		if v, ok := body.Fields["contract_amount_cents"]; ok {
			if f, isNum := v.(float64); isNum && f > 0 {
				c := int64(f)
				contractAmt = &c
			}
		}
		if contractAmt == nil || *contractAmt <= 0 {
			return fmt.Errorf("硬判据 amount_vs_pr 需要合同金额（amount_cents/contract_amount_cents）")
		}
		tol, ok := d.contractAmountTolerance()
		if !ok {
			return fmt.Errorf("合同容差参数未装配（spec/params.json#contract.amount_over_pr_tolerance_percent）")
		}
		limit := *pr.AmountCents * int64(100+tol) / 100
		if *contractAmt > limit {
			action := d.contractOverToleranceAction()
			if action == "require_purchase_change" {
				return fmt.Errorf("合同金额 %d 分超出 PR 预估 %d 分的 %d%% 容差 —— 已超出容差，请先行采购变更（PC）",
					*contractAmt, *pr.AmountCents, tol)
			}
			return fmt.Errorf("合同金额 %d 分超出 PR 预估 %d 分的 %d%% 容差（处置：%s）—— 拒绝",
				*contractAmt, *pr.AmountCents, tol, action)
		}
	}

	// ---- no_self_purchaser（PR/CT 同款，R-27）----
	if severityOf("no_self_purchaser") == "hard" {
		designatedBy := ""
		if v, ok := body.Fields["designated_by"].(string); ok {
			designatedBy = strings.TrimSpace(v)
		}
		if designatedBy != "" && designatedBy == strings.TrimSpace(applicantOpenID) {
			return fmt.Errorf("指定人不能是需求提出人本人（禁止自批、自派、自经办 —— R-27）")
		}
	}
	return nil
}
