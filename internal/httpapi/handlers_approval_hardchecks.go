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
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	// ---- BJ（比价表）----
	"min_three_valid_quotes":               checkBJMinThreeQuotes,
	"quotes_must_be_independent":           checkBJQuotesIndependent,
	"technical_compliance_filled":          checkBJTechnicalCompliance,
	"selected_must_be_valid_and_compliant": checkBJSelectedValidCompliant,
	"not_single_source":                    checkBJNotSingleSource,
	// "selection_reason_immutable"：when=提交后 ⇒ verifyBJPostSubmitImmutability 承载

	// ---- RFQ（询价单）----
	// ★ not_single_source 与 BJ **同名同款共用**；related_pr_must_exist 已在 PR/SS
	// 段注册（共用；RFQ/SS 语义差异＝函数内按 DocType 分流，见该函数注释）。
	"invited_min_by_tier":    checkRFQInvitedMinByTier,
	"deadline_after_send":    checkRFQDeadlineAfterSend,
	"send_evidence_required": checkRFQSendEvidence,

	// ---- SUB（集团提交流转单）----
	"related_docs_complete":            checkSUBRelatedDocsComplete,
	"contract_approved_when_over_1000": checkSUBContractApproved,
	"payee_change_requires_callback":   checkSUBPayeeChangeCallback,
	"tolerance_note_when_over":         checkSUBToleranceNote,
	"handover_receipt_required":        checkSUBHandoverReceipt,
	// "ledger_l06_written"：when=落账后（提交→落账→自检；本单无审批链提交即终态）
	//   ⇒ verifySUBPostLedgerL06 承载（与 GR/QC 同款顺序纪律）
	// soft: submit_deadline_warning —— 超 3 工作日**只预警不阻断**（工具表"计入异常预警"≠不予受理）

	// ---- GR（到货验收单）----
	"related_order_or_record_must_exist":   checkGRRelatedOrderOrRecord,
	"acceptance_group_members_complete":    checkGRAcceptanceGroupMembers,
	"no_ops_supervisor_as_member":          checkGRNoOpsSupervisorMember,
	"no_approver_in_acceptance_group":      checkGRNoApproverInGroup,
	"concession_dual_sign_when_concession": checkGRConcessionDualSign,
	"received_quantity_positive":           checkGRReceivedQuantityPositive,
	// "ledger_l07_written"：when=提交后 ⇒ verifyGRPostSubmitL07 承载（5 列自检）
	// soft: inspection_vs_conclusion_hint —— 只提示不阻断（N-017；提示基础设施随 acceptance.csv）

	// ---- QC（来料检验报告）----
	"related_gr_must_exist":            checkQCRelatedGRExists,
	"inspection_result_required":       checkQCInspectionResult,
	"sample_quantity_pair":             checkQCSampleQuantityPair,
	"defect_description_when_not_pass": checkQCDefectDescription,
	// "l07_inspection_conclusion_written"：when=提交后 ⇒ verifyQCPostSubmitL07 承载（写 L07）

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
		// 提交**时点**判定：白名单而非「含"提交"二字」——
		// "提交后" 与 "落账后（…提交→落账→自检…）" 都含"提交"却不是提交时点，
		// 误入会因未注册而 fail-closed 报错（自检由 verify*Post* 承载）。
		when := strings.TrimSpace(c.When)
		isSubmitMoment := when == "submit" || strings.HasPrefix(when, "submit ") ||
			strings.Contains(when, "提交前")
		if !isSubmitMoment {
			continue // 非提交时点（签署 / 算链 / 终态 / 提交后 / 落账后 / 节点时点）—— 由各自时点承载
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

// checkCTMandatoryClauses 制度第三十五条：各组必备条款**均须有实质内容**（缺任一项不得进入
// 合同审批）。判据：第 1..maxGroup 组（clause_group 标注）在提交时点**至少各有 1 个非空值**。
//
// ★ 组数**不写死 8**（N-031 收口：业务数不得进代码）—— 从 CT 表单的 clause_group
//
//	值域（提交时点字段的标注）推导；某组号无字段定义 ⇒ fail-closed（表单结构坏了必须可见，
//	否则该组永远无实质内容却按"未定义"放过）。maxGroup 与 checks.json#S13 的 min_hits
//	同源同值（S13 锚定同一份表单标注，一处定义两处生效）。
func checkCTMandatoryClauses(_ context.Context, _ Deps, form specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	maxGroup := 0
	groupHasFields := map[int]bool{}
	for _, sec := range form.Sections {
		if strings.TrimSpace(sec.FilledAt) != "" {
			continue
		}
		for _, f := range sec.Fields {
			g := f.ClauseGroup
			if g == 0 {
				continue
			}
			groupHasFields[g] = true
			if g > maxGroup {
				maxGroup = g
			}
			if hHas(provided, f.Name) {
				// 实质内容在下方按组汇总
			}
		}
	}
	if maxGroup == 0 {
		return fmt.Errorf("CT 表单缺 clause_group 标注 —— 必备条款校验无法执行（fail-closed，不许静默通过）")
	}
	filled := map[int]bool{}
	for _, sec := range form.Sections {
		if strings.TrimSpace(sec.FilledAt) != "" {
			continue
		}
		for _, f := range sec.Fields {
			if f.ClauseGroup > 0 && hHas(provided, f.Name) {
				filled[f.ClauseGroup] = true
			}
		}
	}
	for g := 1; g <= maxGroup; g++ {
		if !groupHasFields[g] {
			return fmt.Errorf("合同必备条款第 %d 组在表单中无字段定义（spec 结构异常 —— fail-closed）", g)
		}
		if !filled[g] {
			return fmt.Errorf("合同必备条款第 %d 组无实质内容（制度第三十五条：缺少任一项的不得进入合同审批环节）", g)
		}
	}
	return nil
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
	// ★ 同名判据两语义（spec 逐单写明）：RFQ#related_pr_must_exist 只要求「存在」——
	// RFQ 是 PR 审批链 seq2 的产物（doc_chains.RFQ.parent=PR@rfq），提交时 PR 必然
	// 还在审批中，查 APPROVED 会**拦掉全部合法 RFQ**（误拦）。
	// SS#related_pr_must_exist 才要求「存在且已批准」。
	if body.DocType == "RFQ" {
		return nil
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

// ---------------------------------------------------------------------------
// QC（来料检验报告 · 判据见 spec/forms/QC.json#checks）
// ---------------------------------------------------------------------------

// checkQCRelatedGRExists 关联 GR 必须存在且**等值匹配**（与 related_pr 不同：
// QC 只须存在、不校验 GR 状态 —— 照抄 assert）。
func checkQCRelatedGRExists(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	related := hStr(provided, "related_biz_no")
	if related == "" {
		return fmt.Errorf("related_biz_no 不能为空（须关联到货验收单 GR）")
	}
	gr, err := d.DB.GetInstanceByBizNo(ctx, related)
	if err != nil || gr == nil {
		return fmt.Errorf("关联验收单 %q 不存在 —— 不静默通过（fail-open＝假校验）", related)
	}
	if gr.DocType != "GR" {
		return fmt.Errorf("关联单 %q 不是 GR（当前 doc_type=%s）", related, gr.DocType)
	}
	return nil
}

// checkQCInspectionResult 判定结论必填且 ∈ {合格, 不合格, 让步使用}。
func checkQCInspectionResult(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	v := hStr(provided, "inspection_result")
	if v == "" {
		return fmt.Errorf("判定结论（inspection_result）必填")
	}
	switch v {
	case "合格", "不合格", "让步使用":
		return nil
	default:
		return fmt.Errorf("判定结论取值 %q 不在 {合格, 不合格, 让步使用} 内", v)
	}
}

// checkQCSampleQuantityPair 抽检 ⟺ 抽检数量>0（双向）：抽检缺数量拦、全检填数量也拦。
func checkQCSampleQuantityPair(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	method := hStr(provided, "inspection_method")
	qty, hasQty := hNum(provided, "sample_quantity")
	if method == "抽检" {
		if !hasQty || qty <= 0 {
			return fmt.Errorf("检验方式为「抽检」时必须填写大于 0 的抽检数量（sample_quantity）")
		}
		return nil
	}
	if hasQty && qty > 0 {
		return fmt.Errorf("非抽检方式不得填写抽检数量（sample_quantity 须为空）")
	}
	return nil
}

// checkQCDefectDescription 判定非「合格」⇒ 须填不合格描述。
func checkQCDefectDescription(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	result := hStr(provided, "inspection_result")
	if result == "" || result == "合格" {
		return nil // 空由 inspection_result_required 拦；合格无约束
	}
	if !hHas(provided, "defect_description") {
		return fmt.Errorf("判定为「%s」时必须填写不合格描述（defect_description）", result)
	}
	return nil
}

// verifyQCPostSubmitL07 QC#l07_inspection_conclusion_written（when=提交后）：
// 把本单 inspection_result 写入**关联 GR 的 L07 行** ext_json.inspection_conclusion。
// ★ 失败**可见**（返回错误 ⇒ 消息带 biz_no）——「不静默通过、不得只记日志」。
func (d Deps) verifyQCPostSubmitL07(ctx context.Context, body *approvalSubmitBody, bizNo string) error {
	provided := hbProvided(body)
	gr := hStr(provided, "related_biz_no")
	result := hStr(provided, "inspection_result")
	if gr == "" || result == "" {
		return fmt.Errorf("QC 提交后 L07 写入失败：related_biz_no / inspection_result 缺失（biz_no=%s）", bizNo)
	}
	if err := d.DB.MergeLedgerArchiveExtField(ctx, "L07", gr, "inspection_conclusion", result); err != nil {
		return fmt.Errorf("QC 提交后 L07 写入失败（biz_no=%s，GR=%s）: %w", bizNo, gr, err)
	}
	return nil
}

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

// ---------------------------------------------------------------------------
// GR（到货验收单 · 判据见 spec/forms/GR.json#checks）
// ---------------------------------------------------------------------------

// grGroupKind 把 acceptance_group 归为三组：1=P01 三方 / 2=P02–P04 双方 / 3=P05–P08 双方。
// ★ 兼容连接符变体（`P02-P04` 与 `P02–P04`）；未知取值 ⇒ (0,false) 可见失败。
func grGroupKind(s string) (int, bool) {
	u := strings.ReplaceAll(strings.TrimSpace(s), "–", "-")
	switch {
	case strings.HasPrefix(u, "P01"):
		return 1, true
	case strings.HasPrefix(u, "P02"):
		return 2, true
	case strings.HasPrefix(u, "P05"):
		return 3, true
	default:
		return 0, false
	}
}

// checkGRRelatedOrderOrRecord 关联单须为 **CT 或 BA** 且等值存在。
// ★★ 必须同时接受两种前缀：采一档不签合同 ⇒ 只认 CT 会让采一档验收永远录不进来
//
//	（「那一档永远没有数据」= L03 恒空同族缺陷）。
func checkGRRelatedOrderOrRecord(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	related := hStr(provided, "related_order_or_record_no")
	if related == "" {
		return fmt.Errorf("related_order_or_record_no 不能为空")
	}
	inst, err := d.DB.GetInstanceByBizNo(ctx, related)
	if err != nil || inst == nil {
		return fmt.Errorf("关联单 %q 不存在 —— 不静默通过（fail-open＝假校验）", related)
	}
	if inst.DocType != "CT" && inst.DocType != "BA" {
		return fmt.Errorf("关联单 %q 类型为 %s，只接受 CT（合同）或 BA（采一档备案单）", related, inst.DocType)
	}
	return nil
}

// checkGRAcceptanceGroupMembers 成员构成**逐组精确、双向**（该有的要有、不该有的必须为空）。
func checkGRAcceptanceGroupMembers(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	kind, ok := grGroupKind(hStr(provided, "acceptance_group"))
	if !ok {
		return fmt.Errorf("acceptance_group 取值 %q 无法识别（应为 P01 / P02-P04 / P05-P08）", hStr(provided, "acceptance_group"))
	}
	// 三组的成员矩阵（spec 断言原文）
	memberOf := map[string]bool{ // 字段 → 是否属该组
		"member_purchaser": true,
		"member_qc":        kind == 1 || kind == 2,
		"member_warehouse": kind == 1,
		"member_ops":       kind == 3,
	}
	for field, required := range memberOf {
		present := hHas(provided, field)
		if required && !present {
			return fmt.Errorf("验收组 %s 缺少成员字段 %s", hStr(provided, "acceptance_group"), field)
		}
		if !required && present {
			return fmt.Errorf("验收组 %s 不得包含成员字段 %s（双向精确：不该有的必须为空）", hStr(provided, "acceptance_group"), field)
		}
	}
	return nil
}

// checkGRNoOpsSupervisorMember 成员**均不得为综合运营主管本人**。
// ★★ 必须**按角色判定**（roles.ops_supervisor 的在岗人），不得按部门 ——
//
//	P05–P08 组的「综合运营部人员」是正常成员，按部门判会把他们全部挡住（误拦）。
func checkGRNoOpsSupervisorMember(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	ops, err := d.DB.ChainRoleCandidates(ctx, "综合运营主管", "", false)
	if err != nil {
		return fmt.Errorf("角色解析失败: %w", err)
	}
	blocked := map[string]bool{}
	for _, o := range ops {
		blocked[o.OpenID] = true
		blocked[o.Name] = true // 成员字段可能携带显示名
	}
	for _, f := range []string{"member_purchaser", "member_qc", "member_warehouse", "member_ops"} {
		v := hStr(provided, f)
		if v != "" && blocked[v] {
			return fmt.Errorf("验收组成员 %s 不得为综合运营主管本人（%s）", f, v)
		}
	}
	return nil
}

// checkGRNoApproverInGroup 成员均不得为**该单所属业务链**的审批人。
// ★★ 取不到审批记录 ⇒ **可见失败**（fail-open＝假校验 —— 数据缺一条就让约束整体消失，
//
//	与没写这条校验毫无区别；有意为之：让缺数据可见）。
func checkGRNoApproverInGroup(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	related := hStr(provided, "related_order_or_record_no")
	if related == "" {
		return fmt.Errorf("related_order_or_record_no 为空 —— 无法核对审批人回避（fail-closed）")
	}
	tasks, err := d.DB.ListFlowTasks(ctx, related)
	if err != nil {
		return fmt.Errorf("读取业务链审批记录失败: %w", err)
	}
	if len(tasks) == 0 {
		return fmt.Errorf("关联单 %q 取不到任何审批记录 —— 不放行（fail-open＝假校验）", related)
	}
	// 审批人集合：open_id ＋ 姓名（成员字段可能携带任一形态）
	blocked := map[string]bool{}
	var ids []string
	for _, tk := range tasks {
		if tk.AssigneeOpenID != "" {
			blocked[tk.AssigneeOpenID] = true
			ids = append(ids, tk.AssigneeOpenID)
		}
	}
	if len(ids) > 0 {
		byID, err := d.DB.MapUserRolesByOpenIDs(ctx, ids)
		if err == nil {
			for _, ur := range byID {
				if ur.Name != "" {
					blocked[ur.Name] = true
				}
			}
		}
	}
	for _, f := range []string{"member_purchaser", "member_qc", "member_warehouse", "member_ops"} {
		v := hStr(provided, f)
		if v != "" && blocked[v] {
			return fmt.Errorf("验收组成员 %s 是该单业务链上的审批人（自己批的不能自己验）", f)
		}
	}
	return nil
}

// checkGRConcessionDualSign 让步接收 ⇒ 双签（两人且不同人）；其余结论 ⇒ 双签必须为空。
func checkGRConcessionDualSign(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	conclusion := hStr(provided, "acceptance_conclusion")
	qcSign := hStr(provided, "concession_qc_signer")
	deptSign := hStr(provided, "concession_user_dept_signer")
	if conclusion == "让步接收" {
		if qcSign == "" || deptSign == "" {
			return fmt.Errorf("让步接收须双签（质检与使用部门签署人）均非空")
		}
		if qcSign == deptSign {
			return fmt.Errorf("让步接收双签不得为同一人")
		}
		return nil
	}
	if qcSign != "" || deptSign != "" {
		return fmt.Errorf("非「让步接收」结论不得填写让步双签（concession_* 须为空）")
	}
	return nil
}

// checkGRReceivedQuantityPositive 实收数量非空且 >0（三单匹配的前提）。
func checkGRReceivedQuantityPositive(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	qty, ok := hNum(provided, "received_quantity")
	if !ok || qty <= 0 {
		return fmt.Errorf("实收数量（received_quantity）必须为大于 0 的数")
	}
	return nil
}

// verifyGRPostSubmitL07 GR#ledger_l07_written（when=提交后）：本单的 L07 行
// **5 列**必须已产出：biz_no + related_order_or_record_no + received_quantity +
// acceptance_conclusion + acceptance_members（★ 不含 inspection_conclusion —— 那是 QC 的）。
// 失败可见（错误带 biz_no）——「台账看起来正常但没有这一行」＝三单匹配数据源静默缺失。
//
// ★ 时点观察（随 GR 发起批对齐，已在 COLLAB 回执登记）：现有架构**落账在终态**
// （finalize），而本判据 when=提交后 —— GR 的链形态/落账时点以其发起批的口径为准；
// 函数自检逻辑不变（行与 5 列是否真的产出）。
func (d Deps) verifyGRPostSubmitL07(ctx context.Context, bizNo string) error {
	var related, extRaw string
	var qty any
	err := d.DB.QueryRowContext(ctx, `
SELECT biz_no, COALESCE(ext_json,'{}'), COALESCE(amount_cents,'') FROM t_ledger_archive
WHERE ledger_type='L07' AND biz_no = ?`, bizNo).Scan(&related, &extRaw, &qty)
	if err != nil {
		return fmt.Errorf("GR 提交后 L07 自检失败：行不存在或读取失败（biz_no=%s）: %w", bizNo, err)
	}
	ext := map[string]any{}
	if extRaw != "" && extRaw != "{}" {
		if err := json.Unmarshal([]byte(extRaw), &ext); err != nil {
			return fmt.Errorf("GR 提交后 L07 自检失败：ext_json 损坏（biz_no=%s）: %w", bizNo, err)
		}
	}
	// 5 列逐项非空（biz_no 即行键；其余 4 列在 ext）
	for _, k := range []string{"related_order_or_record_no", "received_quantity", "acceptance_conclusion", "acceptance_members"} {
		v, exists := ext[k]
		if !exists || v == nil {
			return fmt.Errorf("GR 提交后 L07 自检失败：列 %s 未产出（biz_no=%s）——「没有数据」不得与「没有验收」一样", k, bizNo)
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return fmt.Errorf("GR 提交后 L07 自检失败：列 %s 为空（biz_no=%s）——「没有数据」不得与「没有验收」一样", k, bizNo)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// SUB（集团提交流转单 · 判据见 spec/forms/SUB.json#checks）
// ---------------------------------------------------------------------------

// subBizNoRe 识别我方业务单号（前缀-YYMM-####）。
// ★ 前缀白名单＝chain.json 的 11 类单据（含 3 字母 RFQ）—— `[A-Z]{2}` 会把
// 形似串（XX-2610-0001）当有效单号去查，查无 ⇒ 误拦（WB SUB 验收记项，闭环）。
var subBizNoRe = regexp.MustCompile(`(?:BA|PR|SA|RFQ|BJ|SS|CT|PC|GR|QC|SUB)-[0-9]{4}-[0-9]{4}`)

// parseRelatedDocs 从文本清单解析单据号（去重保序）。
func parseRelatedDocs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range subBizNoRe.FindAllString(text, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// checkSUBRelatedDocsComplete 关联单据**逐个等值查存在**（不是非空 —— 文本清单只校验
// 非空等于没校验：写一句「已齐」也能过）；查不到不得 fail-open。
func checkSUBRelatedDocsComplete(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	text := hStr(provided, "related_docs")
	if text == "" {
		return fmt.Errorf("related_docs 不能为空（关联单据清单）")
	}
	ids := parseRelatedDocs(text)
	if len(ids) == 0 {
		return fmt.Errorf("related_docs 中未解析到任何单据号（形如 CT-2609-0001）—— 文本清单必须可逐项校验，不接受「只写已齐」")
	}
	for _, id := range ids {
		inst, err := d.DB.GetInstanceByBizNo(ctx, id)
		if err != nil || inst == nil {
			return fmt.Errorf("关联单 %q 不存在 —— 缺项不得提交（fail-open＝假校验）", id)
		}
	}
	return nil
}

// checkSUBContractApproved ≥1,000 元（**含**，100,000 分整数闭区间）⇒ 合同审批必须已完成。
// ★ 与「≥1,000 元必须签合同」是同一条线的两端；严格大于会让恰好 1,000 元的单漏拦。
func checkSUBContractApproved(ctx context.Context, d Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	amt, ok := hNum(provided, "amount_cents")
	if !ok && body.AmountCents != nil {
		amt = float64(*body.AmountCents)
		ok = true
	}
	if !ok {
		return fmt.Errorf("amount_cents 缺失 —— 无法判定合同审批前置（fail-closed）")
	}
	if amt < 100000 {
		return nil // <1,000 元不适用（采一档不签合同）
	}
	// 系统标记优先；否则到关联清单里找已审批的 CT（等值）
	if v, isBool := provided["contract_approved"].(bool); isBool && v {
		return nil
	}
	for _, id := range parseRelatedDocs(hStr(provided, "related_docs")) {
		if !strings.HasPrefix(id, "CT-") {
			continue
		}
		inst, err := d.DB.GetInstanceByBizNo(ctx, id)
		if err == nil && inst != nil && inst.Status == "APPROVED" {
			return nil
		}
	}
	return fmt.Errorf("金额 %s（≥1,000 元）但关联合同审批未完成 —— 合同审批是提交集团的硬性前置", formatCents(int64(amt)))
}

// checkSUBPayeeChangeCallback 收款账户变更 ⇒ 必须电话回拨留痕（双向；全案唯一反欺诈校验）。
func checkSUBPayeeChangeCallback(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	verified, isBool := provided["payee_account_verified"].(bool)
	if !isBool {
		return fmt.Errorf("payee_account_verified 必须为布尔值（账户是否与合同预留一致）")
	}
	callbackConfirmed, _ := provided["callback_confirmed"].(bool)
	callbackNote := hStr(provided, "callback_note")
	if !verified { // 账户变更/不一致 ⇒ 回拨确认 + 备注必填
		if !callbackConfirmed {
			return fmt.Errorf("收款账户与合同预留不一致/发生变更 —— 必须电话回拨确认（callback_confirmed）")
		}
		if callbackNote == "" {
			return fmt.Errorf("回拨确认必须填写回拨备注（callback_note）—— 留痕是这条校验的全部意义")
		}
		return nil
	}
	// 双向：账户一致时不得填回拨（假留痕会稀释信号）
	if callbackConfirmed || callbackNote != "" {
		return fmt.Errorf("账户未变更时不得填写回拨确认/备注（避免假留痕稀释反欺诈信号）")
	}
	return nil
}

// checkSUBToleranceNote 三单差异超容差 ⇒ 如实说明（**只要求说明、不要求合格** ——
// 最终复核权在集团财务；写成"不许提交"＝替集团做判断，会把真实差异逼成「一致」）。
// 双向：其余结论 ⇒ 说明必须为空。
func checkSUBToleranceNote(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	match := hStr(provided, "three_way_match")
	note := hStr(provided, "tolerance_note")
	if match == "" {
		return fmt.Errorf("three_way_match 必填（三单匹配结论）")
	}
	if match == "差异超容差" {
		if note == "" {
			return fmt.Errorf("差异超容差必须填写 tolerance_note（如实说明，不是自己下结论）")
		}
		return nil
	}
	if note != "" {
		return fmt.Errorf("非「差异超容差」结论不得填写 tolerance_note（双向）")
	}
	return nil
}

// checkSUBHandoverReceipt 移交凭证非空 —— 无凭证则「提交」这件事不成立
// （L06 里会出现「看起来已提交、实际没交」的行，事后无法区分）。
func checkSUBHandoverReceipt(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if !hHas(provided, "handover_receipt") {
		return fmt.Errorf("移交凭证（handover_receipt）非空是提交成立的条件 —— 无凭证视为未提交")
	}
	return nil
}

// verifySUBPostLedgerL06 SUB#ledger_l06_written（when=落账后 ★ 顺序＝提交→落账→自检；
// SUB 无审批链、提交即终态 —— 与 GR/QC 同款顺序纪律）：
//
//	① 同步存档表 6 列：biz_no / related_docs / item_type / amount_cents / payment_method / hunan_completed_at
//	② 运营表至少 submit_group_at 与 handover_receipt 两列已有值
//
// ★ **只校验提交时能确定的列** —— 集团侧 4 列（编号/状态/付款完成日/驳回处置）事后人工登记、
// 提交时必然为空，**不得**算进自检（否则每次提交都失败＝误拦）。
//
// ⚠ 依赖登记（随 SUB 发起批对齐）：hunan_completed_at 的**生产者口径未定**（表单无此字段，
// 见 COLLAB 议题）；运营表种子行（submit_group_at/handover_receipt）需落账时写入 ——
// 两者就位前本自检必失败（**fail-closed 是本意**），SUB 提交通路当前不可达故无误拦。
func (d Deps) verifySUBPostLedgerL06(ctx context.Context, bizNo string) error {
	// ① 存档 6 列：biz_no（行键）+ amount_cents（列）+ 4 个 ext 键
	var extRaw string
	var amount any
	if err := d.DB.QueryRowContext(ctx, `
SELECT COALESCE(ext_json,'{}'), amount_cents FROM t_ledger_archive
WHERE ledger_type='L06' AND biz_no = ?`, bizNo).Scan(&extRaw, &amount); err != nil {
		return fmt.Errorf("SUB 落账自检失败：L06 存档行不存在（biz_no=%s）: %w", bizNo, err)
	}
	ext := map[string]any{}
	if extRaw != "" && extRaw != "{}" {
		if err := json.Unmarshal([]byte(extRaw), &ext); err != nil {
			return fmt.Errorf("SUB 落账自检失败：ext_json 损坏（biz_no=%s）: %w", bizNo, err)
		}
	}
	if amount == nil {
		return fmt.Errorf("SUB 落账自检失败：amount_cents 未产出（biz_no=%s）", bizNo)
	}
	for _, k := range []string{"related_docs", "item_type", "payment_method", "hunan_completed_at"} {
		v, exists := ext[k]
		if !exists || v == nil {
			return fmt.Errorf("SUB 落账自检失败：存档列 %s 未产出（biz_no=%s）", k, bizNo)
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return fmt.Errorf("SUB 落账自检失败：存档列 %s 为空（biz_no=%s）", k, bizNo)
		}
	}
	// ② 运营表两列
	var opsJSON string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT ops_json FROM t_ledger_ops WHERE ledger_type='L06' AND biz_no=?`, bizNo).Scan(&opsJSON); err != nil {
		return fmt.Errorf("SUB 落账自检失败：L06 运营行不存在（biz_no=%s）: %w", bizNo, err)
	}
	ops := map[string]any{}
	if opsJSON != "" && opsJSON != "{}" {
		if err := json.Unmarshal([]byte(opsJSON), &ops); err != nil {
			return fmt.Errorf("SUB 落账自检失败：运营行 ops_json 损坏（biz_no=%s）: %w", bizNo, err)
		}
	}
	for _, k := range []string{"submit_group_at", "handover_receipt"} {
		v, exists := ops[k]
		if !exists || v == nil {
			return fmt.Errorf("SUB 落账自检失败：运营列 %s 未产出（biz_no=%s）—— 无凭证视为未提交的两列必须在落账时写入", k, bizNo)
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return fmt.Errorf("SUB 落账自检失败：运营列 %s 为空（biz_no=%s）", k, bizNo)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// BJ（比价表 · 判据见 spec/forms/BJ.json#checks；权威源＝工具表 采购方式与留痕 R6/R19）
// ---------------------------------------------------------------------------

// countBJQuotes 从文本明细计数报价家数 —— ★ **由明细自动计数，不接受手填**
// （手填必然填成 3：「表格会自己骗自己」）。逐行 + 行内分号分段（能解析的部分）。
func countBJQuotes(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, seg := range strings.FieldsFunc(line, func(r rune) bool {
			return r == '；' || r == ';' || r == '，'
		}) {
			if strings.TrimSpace(seg) != "" {
				n++
			}
		}
	}
	return n
}

// quoteLines 取报价明细的逐行内容（选定单位定位用）。
func quoteLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// checkBJMinThreeQuotes 明细计数 ≥3；与 system 声明的 quote_count 不一致 ⇒ 拦
// （「表上 3 家、明细 2 家」是本判据要抓的核心形态）。
func checkBJMinThreeQuotes(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	quotes := hStr(provided, "quotes")
	if quotes == "" {
		return fmt.Errorf("报价明细（quotes）必填 —— 家数由明细自动计数")
	}
	derived := countBJQuotes(quotes)
	if derived < 3 {
		return fmt.Errorf("报价明细计数 %d 家，少于 3 家 —— 采三档成立条件不足（正确出路：改走竞争性谈判或单一来源通道）", derived)
	}
	if qc, ok := hNum(provided, "quote_count"); ok && int(qc) != derived {
		return fmt.Errorf("quote_count=%d 与明细实际家数 %d 不一致（家数以明细为准，不接受手填）", int(qc), derived)
	}
	return nil
}

// checkBJQuotesIndependent 关联关系**声明**必须为「是」（全部独立）。
// ★ 本判据校验的是声明不是事实（无工商数据源）—— 报错文案不得暗示系统能发现围标。
func checkBJQuotesIndependent(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	v, isBool := provided["all_quotes_independent"].(bool)
	if !isBool {
		return fmt.Errorf("关联关系声明（all_quotes_independent）必须为布尔值")
	}
	if !v {
		return fmt.Errorf("关联关系声明为「否」—— 存在关联报价须剔除关联方，或改走招标／竞争性谈判" +
			"（注：本判据校验的是**显式声明**，系统无工商数据源、不核验事实）")
	}
	return nil
}

// checkBJTechnicalCompliance 技术符合性非空且**逐家**（按明细条数）给出判断。
// ★ 只要求「填了」，不要求「结论正确」—— 技术判断是人的专业判断，系统只让它必须发生。
func checkBJTechnicalCompliance(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	text := hStr(provided, "technical_compliance")
	if text == "" {
		return fmt.Errorf("技术符合性（technical_compliance）必填 —— 逐家给出符合/不符合判断")
	}
	quotesN := countBJQuotes(hStr(provided, "quotes"))
	mentions := strings.Count(text, "符合") // 覆盖「符合」与「不符合」
	if quotesN > 0 && mentions < quotesN {
		return fmt.Errorf("技术符合性判断不足逐家：明细 %d 家，符合/不符合 仅提及 %d 处", quotesN, mentions)
	}
	return nil
}

// checkBJSelectedValidCompliant 选定单位必须出现在明细中，且该行标注**有效**与**技术符合**。
// ★ 防「比价做样子」：陪标 3 家、最后选了没报价/技术不符合的一家 —— 那样前面所有判据都白做。
// ★ 文本明细的代价（known_gaps 第 1 条）：只校验**能解析出来的部分**，标注缺失 ⇒ fail-closed。
func checkBJSelectedValidCompliant(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	selected := hStr(provided, "selected_supplier")
	if selected == "" {
		return fmt.Errorf("选定单位（selected_supplier）必填")
	}
	var hit string
	for _, line := range quoteLines(hStr(provided, "quotes")) {
		if strings.Contains(line, selected) {
			hit = line
			break
		}
	}
	if hit == "" {
		return fmt.Errorf("选定单位 %q 未出现在报价明细中 —— 必须从报价清单里选（不能选没报过价的）", selected)
	}
	valid := strings.Contains(hit, "有效") && !strings.Contains(hit, "无效")
	compliant := strings.Contains(hit, "技术符合") ||
		(strings.Contains(hit, "符合") && !strings.Contains(hit, "不符合"))
	if !valid || !compliant {
		return fmt.Errorf("明细中 %q 所在行未标注为「有效」且「技术符合」（文本明细只校验能解析的部分 —— 标注缺失不放行）", selected)
	}
	return nil
}

// checkBJNotSingleSource 本单存在 ⇒ 采购方式不得为「单一来源」（关闭 SS 遗留互斥缺口：
// 单一来源的前提是凑不出 3 家，与本单语义不可能同时成立 —— 答案就在 procure_method 一个字段上）。
func checkBJNotSingleSource(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	if hStr(provided, "procure_method") == "单一来源" {
		return fmt.Errorf("比价表的采购方式不得为「单一来源」—— 无询比价即无比价表（应改走单一来源通道的单据是 SS）")
	}
	return nil
}

// verifyBJPostSubmitImmutability BJ#selection_reason_immutable（when=提交后）：
// selected_reason / procure_method / selected_supplier **提交后不得修改**。
// ★ 工具表 R19「不得事后补写」在系统里的唯一可执行形态 —— 否则「事前形成」在系统上不成立。
// 实现＝提交即冻结自检：回读实例 ext 与提交值比对；结构性保障＝本系统**不存在**
// 任何修改实例业务字段的接口（路由断言见测试），无路径 ⇒ 不可改。
func (d Deps) verifyBJPostSubmitImmutability(ctx context.Context, body *approvalSubmitBody, bizNo string) error {
	inst, err := d.DB.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		return fmt.Errorf("BJ 提交后不可改自检失败：实例读取失败（biz_no=%s）: %w", bizNo, err)
	}
	ext := map[string]any{}
	if inst.ExtJSON != "" && inst.ExtJSON != "{}" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &ext); err != nil {
			return fmt.Errorf("BJ 提交后不可改自检失败：ext_json 损坏（biz_no=%s）: %w", bizNo, err)
		}
	}
	provided := hbProvided(body)
	for _, k := range []string{"selected_reason", "procure_method", "selected_supplier"} {
		submitted := hStr(provided, k)
		frozen, _ := ext[k].(string)
		if submitted != "" && frozen != submitted {
			return fmt.Errorf("BJ 提交后不可改自检失败：%s 冻结值与提交值不一致（biz_no=%s）", k, bizNo)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// RFQ（询价单 · 判据见 spec/forms/RFQ.json#checks；★ 唯一带 parent 的单据：
// doc_chains.RFQ.parent = "PR@rfq"；★ ledger=[] 不落账 ⇒ 存在证明全在附件）
// ---------------------------------------------------------------------------

// rfqThresholdOf 按档位取邀请家数门槛（★ 易错点①：不是一律 3 家）。
// 询比价（采三档）⇒ ≥3（制度第二十条/工具表 R6）；
// 直采（采二档「报价比选」语境，R5「≥1 家」）⇒ ≥1；
// 单一来源 ⇒ 返回 (0,false)：本条放行，由 not_single_source 专拦（报错更准）；
// 其余值（招标/竞争性谈判/未知）⇒ fail-closed：RFQ 通道不适用这些方式，不静默放行。
func rfqThresholdOf(procureMethod string) (int, bool) {
	switch procureMethod {
	case "询比价":
		return 3, true
	case "直采":
		return 1, true
	case "单一来源":
		return 0, false
	default:
		return -1, false // 不可判定 ⇒ fail-closed
	}
}

// checkRFQInvitedMinByTier ★ 门槛随档位 + 家数由明细计数（BJ 同款，不接受手填）。
// 明细 invited_suppliers 逐行/分号/逗号计数，与 system 声明的 invited_count 交叉核对。
func checkRFQInvitedMinByTier(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	threshold, applicable := rfqThresholdOf(hStr(provided, "procure_method"))
	if threshold == 0 && !applicable {
		return nil // 单一来源：本条放行，not_single_source 专拦
	}
	if threshold < 0 {
		return fmt.Errorf("采购方式 %q 不适用于询价通道，无法判定邀请家数门槛（RFQ 只接受 询比价/直采）",
			hStr(provided, "procure_method"))
	}
	detail := hStr(provided, "invited_suppliers")
	if strings.TrimSpace(detail) == "" {
		return fmt.Errorf("邀请单位明细（invited_suppliers）必填 —— 家数由明细自动计数，不接受手填")
	}
	derived := countBJQuotes(detail)
	if derived < threshold {
		if threshold == 3 {
			return fmt.Errorf("采三档询比价须邀请不少于 3 家，明细计数 %d 家", derived)
		}
		return fmt.Errorf("报价比选须不少于 1 家，明细计数 %d 家", derived)
	}
	if ic, ok := hNum(provided, "invited_count"); ok && int(ic) != derived {
		return fmt.Errorf("invited_count=%d 与明细实际家数 %d 不一致（家数以明细为准，不接受手填）", int(ic), derived)
	}
	return nil
}

// checkRFQDeadlineAfterSend quote_deadline 必须晚于 send_date（date 折算当日 00:00）。
// ★ 防回溯性数据：「截止早于发出」现实成因只有事后倒填（与 BJ#selection_reason_immutable
// 同族：一个防「改」一个防「补」）。解析失败 ⇒ fail-closed（假格式不得绕过比较）。
func checkRFQDeadlineAfterSend(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	deadlineStr := hStr(provided, "quote_deadline")
	sendStr := hStr(provided, "send_date")
	if deadlineStr == "" || sendStr == "" {
		return fmt.Errorf("报价截止时间与询价发出日期均必填")
	}
	var deadline time.Time
	var err error
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if deadline, err = time.Parse(layout, deadlineStr); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("报价截止时间 %q 无法解析（解析失败不放行 —— 防倒填用假格式绕过比较）", deadlineStr)
	}
	sendStart, err := time.Parse("2006-01-02", sendStr)
	if err != nil {
		return fmt.Errorf("询价发出日期 %q 无法解析（解析失败不放行）", sendStr)
	}
	if !deadline.After(sendStart) {
		return fmt.Errorf("报价截止时间 %s 不得早于询价发出日期 %s（防事后倒填）",
			deadlineStr, sendStr)
	}
	return nil
}

// checkRFQSendEvidence rfq_file 与 send_evidence 均非空。
// ★★ 本单不落台账（ledger=[]）⇒ 这条判据是它唯一的「存在证明」：
// 工具表 R2「无留存即视为未执行程序」—— 两项可空则本单与「根本没询价」无法区分。
func checkRFQSendEvidence(_ context.Context, _ Deps, _ specload.FormDoc, body *approvalSubmitBody, _ string) error {
	provided := hbProvided(body)
	for _, k := range []string{"rfq_file", "send_evidence"} {
		v, _ := provided[k].(string)
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("未留存询价单与发出证据不得提交（%s 为空 —— 工具表：无留存即视为未执行程序）", k)
		}
	}
	return nil
}

// hasFormCheck 表单是否声明了指定 id 的判据。
func hasFormCheck(form specload.FormDoc, id string) bool {
	for _, c := range form.Checks {
		if c.ID == id {
			return true
		}
	}
	return false
}
