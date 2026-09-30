package httpapi

// N-027 · CT / SS / PC 服务端 hard 判据层 —— **规则从 forms/*.json#checks 驱动**（判据＝数据，
// 与 spec/checks.json 同思路；代码只提供每条 check id 的求值函数，不复制规则文本）。
//
// ★ 分派纪律：
//   - 只执行 `severity=hard` 且 `when` 属提交时点（"submit" 或 "提交前"）的判据；
//   - `idempotency_key` 由 Idempotency-Key 机制承载（跳过，机制已有用例）；
//   - `contract_no_format` 需要**生成后**的 biz_no ⇒ 走 verifyPostSubmitHard（提交后校验）；
//   - ★ **未注册的提交时点 hard 判据 ⇒ 直接报错**（fail-closed：不许静默通过 ——
//     「写了校验其实没执行」正是 N-015/N-026 同族事故）；
//   - soft 判据：按 N-017 口径只提示不阻断（本层不执行；提示文案随 acceptance.csv）。
// ★ 跨单据：`no_self_purchaser` 在 PR/CT/SS/PC 四处同款 —— **只此一个函数**（防漂移）。

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// hardCheckFn 单条 hard 判据的求值：返回 nil=放行；非 nil=可见拒绝（40000）。
type hardCheckFn func(ctx context.Context, d Deps, form specload.FormDoc, body *approvalSubmitBody, applicantOpenID string) error

// submitHardChecks 提交时点 hard 判据注册表（id → 求值函数）。
var submitHardChecks = map[string]hardCheckFn{
	// ---- CT ----
	"mandatory_clauses_complete":  checkCTMandatoryClauses,
	"payee_name_matches_supplier": checkCTPayeeMatchesSupplier,
	"prepay_pair":                 checkCTPrepayPair,
	"sole_source_link":            checkCTSoleSourceLink,
	"tier3_quote_link":            checkCTTier3QuoteLink,
	"non_template_reason":         checkCTNonTemplateReason,
	"equipment_after_sales":       checkCTEquipmentAfterSales,
	"tech_opinion_needed":         checkCTTechOpinionNeeded,
	"amount_vs_pr":                checkCTAmountVsPR,
	// ---- SS ----
	"uniqueness_and_price_required":          checkSSUniquenessPrice,
	"sole_source_attachment_when_authorized": checkSSAuthorizedAttachment,
	"related_pr_must_exist":                  checkRelatedPRMustExist,
	// ---- PC ----
	"contract_no_required_and_exists":   checkPCContractExists,
	"change_amount_nonzero":             checkPCChangeNonzero,
	"reset_as_new_purchase_block":       checkPCResetBlock,
	"special_explanation_required":      checkPCSpecialExplanation,
	"tech_opinion_for_engineering":      checkPCTechOpinionEngineering,
	"new_supplier_when_supplier_change": checkPCNewSupplier,
	// ---- 跨单据同款（★ 唯一实现，四处共用）----
	"no_self_purchaser": checkNoSelfPurchaser,
}

// evaluateHardChecks 遍历 form.checks 执行提交时点 hard 判据（N-025 的 amount_vs_pr /
// no_self_purchaser 已并入本注册表；本函数为唯一入口）。
func (d Deps) evaluateHardChecks(ctx context.Context, form specload.FormDoc, body *approvalSubmitBody, applicantOpenID string) error {
	for _, c := range form.Checks {
		if c.Severity != "hard" {
			continue
		}
		when := c.When
		if !strings.Contains(when, "submit") && !strings.Contains(when, "提交") {
			continue // 非提交时点（签署 / 算链 / 终态 / 节点时点）—— 由各自时点承载
		}
		switch c.ID {
		case "idempotency_key":
			continue // 由 Idempotency-Key 机制承载（M4 已有用例）
		case "contract_no_format":
			continue // 需要生成后的 biz_no ⇒ verifyPostSubmitHard
		}
		fn, ok := submitHardChecks[c.ID]
		if !ok {
			// ★ fail-closed：hard 判据没有求值器 = 「声明了没执行」，必须可见失败
			return fmt.Errorf("hard 判据 %q 未实现求值器（不许静默通过 —— 请在 submitHardChecks 注册）", c.ID)
		}
		if err := fn(ctx, d, form, body, applicantOpenID); err != nil {
			return err
		}
	}
	return nil
}

// verifyPostSubmitHard 提交成功后的 hard 判据（需要生成后的 biz_no）。
// 目前仅 CT#contract_no_format —— 我方生成器恒产 CT-YYMM-####，此处是**自检**：
// 不匹配 ⇒ 内部一致性被破坏 ⇒ 可见 500（带 biz_no 供运维定位），绝不静默。
func (d Deps) verifyPostSubmitHard(form specload.FormDoc, bizNo string) error {
	ctPattern := regexp.MustCompile(`^CT-\d{4}-\d{4}$`)
	for _, c := range form.Checks {
		if c.ID == "contract_no_format" && c.Severity == "hard" {
			if !ctPattern.MatchString(bizNo) {
				return fmt.Errorf("contract_no_format 自检失败：生成的 biz_no=%q 不匹配 ^CT-\\d{4}-\\d{4}$", bizNo)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 值访问
// ---------------------------------------------------------------------------

func hbProvided(body *approvalSubmitBody) map[string]any {
	return mergeProvidedFields(body,
		firstNonEmptyStr(body.UsageCategoryL1, body.PurposeClassL1),
		firstNonEmptyStr(body.UsageCategoryL2, body.PurposeClassL2))
}

func hStr(provided map[string]any, name string) string {
	s, _ := provided[name].(string)
	return strings.TrimSpace(s)
}

func hNum(provided map[string]any, name string) (float64, bool) {
	switch v := provided[name].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func hHas(provided map[string]any, name string) bool {
	v, ok := provided[name]
	if !ok || v == nil {
		return false
	}
	if s, isStr := v.(string); isStr {
		return strings.TrimSpace(s) != ""
	}
	return true
}

// ---------------------------------------------------------------------------
// CT
// ---------------------------------------------------------------------------

// checkCTMandatoryClauses 制度第三十五条：8 组必备条款**均须有实质内容**（缺任一项不得进入
// 合同审批）。判据：第 1..8 组（clause_group 标注）在提交时点**至少各有 1 个非空值**。
func checkCTMandatoryClauses(_ context.Context, _ Deps, form specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	groups := map[int]bool{} // 组号 → 是否已有实质内容
	for _, sec := range form.Sections {
		if strings.TrimSpace(sec.FilledAt) != "" {
			continue
		}
		for _, f := range sec.Fields {
			g := clauseGroupOf(f)
			if g == 0 {
				continue
			}
			if hHas(provided, f.Name) {
				groups[g] = true
			}
		}
	}
	for g := 1; g <= 8; g++ {
		if !groups[g] {
			return fmt.Errorf("合同必备条款第 %d 组无实质内容（制度第三十五条：缺少任一项的不得进入合同审批环节）", g)
		}
	}
	return nil
}

// clauseGroupOf 从字段扩展信息取 clause_group —— FormDoc.FieldDoc 未建模该键，
// 经 Raw 不便；批 1 做法：FieldDoc 增加通用承载？—— 不改 specload 结构的前提下，
// 从 form 的原始 JSON 不可得 ⇒ 用 check 的语义映射：CT 的 clause_group 只在
// FieldDoc 需要。★ 实现见 specload.FieldDoc.ClauseGroup（本函数读它）。
func clauseGroupOf(f specload.FieldDoc) int {
	return f.ClauseGroup
}

func checkCTPayeeMatchesSupplier(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if hStr(provided, "payee_name") != hStr(provided, "supplier") {
		return fmt.Errorf("收款人（payee_name=%q）必须与供应商（supplier=%q）一致", hStr(provided, "payee_name"), hStr(provided, "supplier"))
	}
	return nil
}

func checkCTPrepayPair(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if r, ok := hNum(provided, "prepay_ratio"); !ok || r <= 0 {
		return nil // 无预付 ⇒ 无约束
	}
	if !hHas(provided, "prepay_guarantee_kind") || !hHas(provided, "prepay_guarantee_file") {
		return fmt.Errorf("约定预付（prepay_ratio>0）时必须同时提供对价保障方式与保障附件")
	}
	return nil
}

func checkCTSoleSourceLink(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if hStr(provided, "procure_method") == "单一来源" && !hHas(provided, "linked_ss_biz_no") {
		return fmt.Errorf("采购方式为「单一来源」时必须关联网由理由书（linked_ss_biz_no）")
	}
	return nil
}

func checkCTTier3QuoteLink(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	amt, ok := hNum(provided, "contract_amount_cents")
	if !ok {
		if body.AmountCents != nil {
			amt = float64(*body.AmountCents)
		} else {
			return nil
		}
	}
	if amt > 500000 && !hHas(provided, "linked_bj_biz_no") {
		return fmt.Errorf("合同额超过 5,000 元（采三档）时必须关联比价表（linked_bj_biz_no）")
	}
	return nil
}

func checkCTNonTemplateReason(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if v, ok := provided["is_standard_template"].(bool); ok && !v && !hHas(provided, "non_template_reason") {
		return fmt.Errorf("未使用公司标准范本时必须填写 non_template_reason")
	}
	return nil
}

func checkCTEquipmentAfterSales(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if hStr(provided, "usage_category_l1") != "P04" {
		return nil
	}
	for _, f := range []string{"warranty_period_months", "response_time_hours", "spare_parts_commitment"} {
		if !hHas(provided, f) {
			return fmt.Errorf("设备类（P04）合同必须填写 %s", f)
		}
	}
	return nil
}

func checkCTTechOpinionNeeded(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if hStr(provided, "usage_category_l1") != "P04" {
		return nil
	}
	amt, ok := hNum(provided, "contract_amount_cents")
	if !ok && body.AmountCents != nil {
		amt = float64(*body.AmountCents)
		ok = true
	}
	if ok && amt >= 2000000 && !hHas(provided, "tech_review_opinion") {
		return fmt.Errorf("设备类合同额 ≥2 万元时必须附技术评审意见（tech_review_opinion）")
	}
	return nil
}

func checkCTAmountVsPR(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	related := hStr(provided, "related_biz_no")
	if related == "" {
		return fmt.Errorf("硬判据 amount_vs_pr 需要 related_biz_no（关联 PR 单号）才能校验合同金额容差")
	}
	pr, err := d.DB.GetInstanceByBizNo(ctx, related)
	if err != nil || pr.AmountCents == nil || *pr.AmountCents <= 0 {
		return fmt.Errorf("硬判据 amount_vs_pr 无法关联 PR %q（不存在或无预估金额）—— 不放行（fail-open＝假校验）", related)
	}
	contractAmt := body.AmountCents
	if v, ok := hNum(provided, "contract_amount_cents"); ok && v > 0 {
		c := int64(v)
		contractAmt = &c
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
	return nil
}

// ---------------------------------------------------------------------------
// SS
// ---------------------------------------------------------------------------

func checkSSUniquenessPrice(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if !hHas(provided, "uniqueness_statement") || !hHas(provided, "price_evidence") {
		return fmt.Errorf("单一来源须同时提供唯一性说明（uniqueness_statement）与价格依据（price_evidence）")
	}
	return nil
}

func checkSSAuthorizedAttachment(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if hStr(provided, "uniqueness_basis") == "独家代理／授权" && !hHas(provided, "sole_source_attachment") {
		return fmt.Errorf("唯一性依据为「独家代理／授权」时必须附授权文件（sole_source_attachment）")
	}
	return nil
}

// checkRelatedPRMustExist SS/CT 共用：关联 PR 必须存在且**已批准**（不 fail-open）。
func checkRelatedPRMustExist(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	related := hStr(provided, "related_biz_no")
	if related == "" {
		return fmt.Errorf("related_biz_no 不能为空（须关联已批准的物资采购申请单）")
	}
	pr, err := d.DB.GetInstanceByBizNo(ctx, related)
	if err != nil || pr == nil {
		return fmt.Errorf("关联 PR %q 不存在 —— 可见失败（fail-open＝假校验）", related)
	}
	if pr.DocType != "PR" {
		return fmt.Errorf("关联单 %q 不是 PR（当前 doc_type=%s）", related, pr.DocType)
	}
	if pr.Status != "APPROVED" {
		return fmt.Errorf("关联 PR %q 状态为 %s，必须已批准（APPROVED）方可继续", related, pr.Status)
	}
	return nil
}

// ---------------------------------------------------------------------------
// PC
// ---------------------------------------------------------------------------

// checkPCContractExists 合同号非空且在 L04 **等值**命中（★ 绝不前缀匹配 ——
// 前缀会让 CT-1 捞到 CT-1X）。
func checkPCContractExists(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	contractNo := hStr(provided, "contract_no")
	if contractNo == "" {
		return fmt.Errorf("contract_no 不能为空")
	}
	var n int
	// SQL 等值比较（无 LIKE / 前缀语义）
	if err := d.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type = 'L04' AND biz_no = ?`,
		contractNo).Scan(&n); err != nil {
		return fmt.Errorf("合同台账查询失败: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("合同号 %q 在合同台账（L04）中无等值记录（不做前缀匹配）", contractNo)
	}
	return nil
}

func checkPCChangeNonzero(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	v, ok := hNum(provided, "change_amount_cents")
	if !ok {
		return fmt.Errorf("change_amount_cents 必填") // 金额变更必须显式给出
	}
	if v == 0 {
		return fmt.Errorf("变更金额不能为 0（change_amount_cents != 0）")
	}
	return nil
}

func checkPCResetBlock(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	newTotal, okNew := hNum(provided, "new_total_cents")
	orig, okOrig := hNum(provided, "original_contract_amount_cents")
	if !okNew || !okOrig {
		return nil // 缺失由必填校验负责；此处只判已给出的数
	}
	if newTotal > orig*1.5 {
		return fmt.Errorf("变更后总额超过原合同 150%%（%g > %g×1.5）—— 视为新采购，须重走全套流程", newTotal, orig)
	}
	return nil
}

func checkPCSpecialExplanation(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	total, okTotal := hNum(provided, "total_change_cents")
	orig, okOrig := hNum(provided, "original_contract_amount_cents")
	if !okTotal || !okOrig {
		return nil
	}
	if total > orig*0.3 && !hHas(provided, "special_explanation") {
		return fmt.Errorf("变更总额超过原合同 30%% 须填写专项说明（special_explanation）")
	}
	return nil
}

func checkPCTechOpinionEngineering(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if v, ok := provided["is_engineering_category"].(bool); ok && v {
		if !hHas(provided, "tech_opinion") || !hHas(provided, "tech_opinion_by") {
			return fmt.Errorf("工程类变更须填写技术意见与出具人（tech_opinion / tech_opinion_by）")
		}
	}
	return nil
}

func checkPCNewSupplier(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if hStr(provided, "change_type") != "供应商变更" {
		return nil
	}
	if !hHas(provided, "new_supplier") {
		return fmt.Errorf("供应商变更必须填写 new_supplier")
	}
	if hStr(provided, "new_supplier") == hStr(provided, "original_supplier") {
		return fmt.Errorf("new_supplier 不得与原供应商相同")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 跨单据同款（★ 唯一实现）
// ---------------------------------------------------------------------------

// checkNoSelfPurchaser **禁止自批自派自经办**（R-27 · PR/CT/SS/PC 四处同款）——
// 拦截条件＝`designated_by == 需求提出人`（指定人是申请人本人）；
// 上级领导指派时经办人可以是申请人（放行）。designated_by 未提供 ⇒ 跳过
// （该字段在审批时点填报，提交时点通常缺省；审批时点承载＝N-015 批 2）。
func checkNoSelfPurchaser(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, applicantOpenID string) error {
	provided := hbProvided(body)
	designatedBy := hStr(provided, "designated_by")
	if designatedBy != "" && designatedBy == strings.TrimSpace(applicantOpenID) {
		return fmt.Errorf("指定人不能是需求提出人本人（禁止自批、自派、自经办 —— R-27）")
	}
	return nil
}
