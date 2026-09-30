package httpapi

// N-027 验收：CT/SS/PC 提交时点 hard 判据 —— 每条「应拦 + 应放行」双用例。
// ★ 规则从 forms/*.json#checks 驱动（本文件只构造输入调用注册表函数，不复制规则）。

import (
	"context"
	"strings"
	"testing"
	"time"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

func hcDB(t *testing.T) *store.DB {
	t.Helper()
	db := storetest.NewDB(t)
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func hcBody(amount *int64, fields map[string]any) *approvalSubmitBody {
	return &approvalSubmitBody{DocType: "CT", AmountCents: amount, Fields: fields}
}

// mustPass / mustBlock 断言助手（每条判据两用例）。
func mustBlock(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s：应拦，却放行", name)
	}
}
func mustPass(t *testing.T, name string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s：应放行，却拦截：%v", name, err)
	}
}

func ctFullClauseFields() map[string]any {
	return map[string]any{
		"subject_spec": "煅后石油焦", "quantity": 10, "unit": "吨",
		"unit_price_cents": 5000, "price_is_tax_included": true, "tax_rate_percent": 13,
		"payment_terms": "货到付款", "delivery_date": "2026-11-01", "delivery_place": "岳阳",
		"shipping_duty": "供方", "acceptance_standard": "国标", "nonconformity_handling": "退换",
		"warranty_period_months": 12, "payee_name": "甲供", "payee_account": "6222…",
		"payee_bank": "工行", "breach_liance": "", "breach_liability": "按合同法", "dispute_resolution": "岳阳仲裁",
	}
}

// ---------------- CT ----------------

func TestCTMandatoryClauses(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["CT"]
	full := ctFullClauseFields()

	mustPass(t, "8组齐", checkCTMandatoryClauses(context.Background(), d, form, hcBody(nil, full), "ou_app"))

	missing := map[string]any{}
	for k, v := range full {
		missing[k] = v
	}
	delete(missing, "dispute_resolution") // 第 8 组两字段都拿掉（组=breach+dispute）
	delete(missing, "breach_liability")
	err := checkCTMandatoryClauses(context.Background(), d, form, hcBody(nil, missing), "ou_app")
	mustBlock(t, "第8组空", err)
	if err != nil && !strings.Contains(err.Error(), "第 8 组") {
		t.Errorf("应指明第 8 组：%v", err)
	}
}

func TestCTPayeeMatchesSupplier(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["CT"]
	base := func(payee string) map[string]any {
		f := ctFullClauseFields()
		f["supplier"] = "甲供"
		f["payee_name"] = payee
		return f
	}
	mustPass(t, "收款人=供应商", checkCTPayeeMatchesSupplier(context.Background(), d, form, hcBody(nil, base("甲供")), "ou_app"))
	mustBlock(t, "收款人≠供应商", checkCTPayeeMatchesSupplier(context.Background(), d, form, hcBody(nil, base("乙供")), "ou_app"))
}

func TestCTPrepayPair(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["CT"]
	noPrepay := ctFullClauseFields()
	mustPass(t, "无预付", checkCTPrepayPair(context.Background(), d, form, hcBody(nil, noPrepay), "ou_app"))

	prepayNoFile := ctFullClauseFields()
	prepayNoFile["prepay_ratio"] = 0.3
	prepayNoFile["prepay_guarantee_kind"] = "保函"
	mustBlock(t, "有预付无附件", checkCTPrepayPair(context.Background(), d, form, hcBody(nil, prepayNoFile), "ou_app"))

	prepayFull := ctFullClauseFields()
	prepayFull["prepay_ratio"] = 0.3
	prepayFull["prepay_guarantee_kind"] = "保函"
	prepayFull["prepay_guarantee_file"] = "att-1"
	mustPass(t, "有预付有附件", checkCTPrepayPair(context.Background(), d, form, hcBody(nil, prepayFull), "ou_app"))
}

func TestCTSoleSourceAndTier3Links(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["CT"]

	f1 := ctFullClauseFields()
	f1["procure_method"] = "询比价"
	mustPass(t, "非单一来源无链接", checkCTSoleSourceLink(context.Background(), d, form, hcBody(nil, f1), "ou_app"))

	f2 := ctFullClauseFields()
	f2["procure_method"] = "单一来源"
	mustBlock(t, "单一来源缺理由书", checkCTSoleSourceLink(context.Background(), d, form, hcBody(nil, f2), "ou_app"))

	f3 := ctFullClauseFields()
	f3["procure_method"] = "单一来源"
	f3["linked_ss_biz_no"] = "SS-2609-0001"
	mustPass(t, "单一来源带链接", checkCTSoleSourceLink(context.Background(), d, form, hcBody(nil, f3), "ou_app"))

	f4 := ctFullClauseFields()
	amt60 := int64(600000)
	mustBlock(t, "采三档缺比价表", checkCTTier3QuoteLink(context.Background(), d, form, hcBody(&amt60, f4), "ou_app"))

	f5 := ctFullClauseFields()
	f5["linked_bj_biz_no"] = "BJ-2609-0001"
	mustPass(t, "采三档带比价表", checkCTTier3QuoteLink(context.Background(), d, form, hcBody(&amt60, f5), "ou_app"))

	f6 := ctFullClauseFields()
	amt40 := int64(400000)
	mustPass(t, "采二档无须比价表", checkCTTier3QuoteLink(context.Background(), d, form, hcBody(&amt40, f6), "ou_app"))
}

func TestCTNonTemplateAndEquipmentAndTech(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["CT"]

	// 非范本须理由
	t1 := ctFullClauseFields()
	t1["is_standard_template"] = false
	mustBlock(t, "非范本缺理由", checkCTNonTemplateReason(context.Background(), d, form, hcBody(nil, t1), "ou_app"))
	t1["non_template_reason"] = "对方版本"
	mustPass(t, "非范本有理由", checkCTNonTemplateReason(context.Background(), d, form, hcBody(nil, t1), "ou_app"))
	t2 := ctFullClauseFields()
	t2["is_standard_template"] = true
	mustPass(t, "用范本无需理由", checkCTNonTemplateReason(context.Background(), d, form, hcBody(nil, t2), "ou_app"))

	// P04 售后三件套
	e1 := ctFullClauseFields()
	e1["usage_category_l1"] = "P04"
	mustBlock(t, "P04 缺质保", checkCTEquipmentAfterSales(context.Background(), d, form, hcBody(nil, e1), "ou_app"))
	e1["response_time_hours"] = 24
	e1["spare_parts_commitment"] = "备件5年"
	mustPass(t, "P04 齐", checkCTEquipmentAfterSales(context.Background(), d, form, hcBody(nil, e1), "ou_app"))
	e2 := ctFullClauseFields()
	e2["usage_category_l1"] = "P01"
	mustPass(t, "非P04不拦", checkCTEquipmentAfterSales(context.Background(), d, form, hcBody(nil, e2), "ou_app"))

	// P04 ≥2万 技术评审
	amt200 := int64(2000000)
	g1 := ctFullClauseFields()
	g1["usage_category_l1"] = "P04"
	mustBlock(t, "P04大额缺技评", checkCTTechOpinionNeeded(context.Background(), d, form, hcBody(&amt200, g1), "ou_app"))
	g1["tech_review_opinion"] = "同意"
	mustPass(t, "P04大额带技评", checkCTTechOpinionNeeded(context.Background(), d, form, hcBody(&amt200, g1), "ou_app"))
	g2 := ctFullClauseFields()
	g2["usage_category_l1"] = "P04"
	amt10 := int64(100000)
	mustPass(t, "P04小额不拦", checkCTTechOpinionNeeded(context.Background(), d, form, hcBody(&amt10, g2), "ou_app"))
}

func TestCTContractNoFormatPostSubmit(t *testing.T) {
	form := metaTestBundle(t).Forms["CT"]
	d := Deps{}
	mustPass(t, "生成号合规", d.verifyPostSubmitHard(form, "CT-2609-0007"))
	mustBlock(t, "生成号异常", d.verifyPostSubmitHard(form, "CT-XX-7"))
}

// ---------------- SS ----------------

func TestSSUniquenessPriceAndAuthorizedAttachment(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["SS"]

	mustBlock(t, "缺唯一性说明", checkSSUniquenessPrice(context.Background(), d, form,
		hcBody(nil, map[string]any{"price_evidence": "报价单"}), "ou_app"))
	mustBlock(t, "缺价格依据", checkSSUniquenessPrice(context.Background(), d, form,
		hcBody(nil, map[string]any{"uniqueness_statement": "独家"}), "ou_app"))
	mustPass(t, "两者齐", checkSSUniquenessPrice(context.Background(), d, form,
		hcBody(nil, map[string]any{"uniqueness_statement": "独家", "price_evidence": "报价单"}), "ou_app"))

	mustBlock(t, "授权类缺附件", checkSSAuthorizedAttachment(context.Background(), d, form,
		hcBody(nil, map[string]any{"uniqueness_basis": "独家代理／授权"}), "ou_app"))
	mustPass(t, "授权类带附件", checkSSAuthorizedAttachment(context.Background(), d, form,
		hcBody(nil, map[string]any{"uniqueness_basis": "独家代理／授权", "sole_source_attachment": "a1"}), "ou_app"))
	mustPass(t, "其他依据不拦", checkSSAuthorizedAttachment(context.Background(), d, form,
		hcBody(nil, map[string]any{"uniqueness_basis": "技术独有"}), "ou_app"))
}

func TestSSRelatedPRMustExist(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["SS"]
	ctx := context.Background()
	seedPR := func(biz, status string) {
		if _, err := db.ExecContext(ctx, `
INSERT INTO t_instance (instance_code, approval_code, doc_type, biz_no, status,
  applicant_open_id, amount_cents, source, created_at, updated_at)
VALUES (?, 'ac-pr-0001', 'PR', ?, ?, 'ou_app', 100000, 'flow', ?, ?)`,
			"I-"+biz, biz, status,
			time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	seedPR("PR-2609-0010", "APPROVED")
	seedPR("PR-2609-0011", "PENDING")

	mustBlock(t, "缺关联", checkRelatedPRMustExist(ctx, d, form, hcBody(nil, map[string]any{}), "ou_app"))
	mustBlock(t, "关联不存在", checkRelatedPRMustExist(ctx, d, form,
		hcBody(nil, map[string]any{"related_biz_no": "PR-0000-9999"}), "ou_app"))
	mustBlock(t, "未批准", checkRelatedPRMustExist(ctx, d, form,
		hcBody(nil, map[string]any{"related_biz_no": "PR-2609-0011"}), "ou_app"))
	mustPass(t, "已批准", checkRelatedPRMustExist(ctx, d, form,
		hcBody(nil, map[string]any{"related_biz_no": "PR-2609-0010"}), "ou_app"))
}

// ---------------- PC ----------------

func TestPCContractExactMatch(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["PC"]
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// L04 只有「带后缀」的合同号 —— 等值匹配不得被前缀捞中
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_ledger_archive (ledger_type, biz_no, instance_code, source_doc_type, department,
  applicant_open_id, ext_json, created_at, updated_at)
VALUES ('L04', 'CT-2609-0001X', 'I-X', 'CT', '生产部', 'ou_app', '{}', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}

	mustBlock(t, "合同号为空", checkPCContractExists(ctx, d, form, hcBody(nil, map[string]any{}), "ou_app"))
	// ★ 前缀陷阱：'CT-2609-0001' 是 'CT-2609-0001X' 的前缀 —— 等值匹配必须拦
	mustBlock(t, "前缀不得捞中", checkPCContractExists(ctx, d, form,
		hcBody(nil, map[string]any{"contract_no": "CT-2609-0001"}), "ou_app"))

	// 精确存在 → 放行
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_ledger_archive (ledger_type, biz_no, instance_code, source_doc_type, department,
  applicant_open_id, ext_json, created_at, updated_at)
VALUES ('L04', 'CT-2609-0002', 'I-Y', 'CT', '生产部', 'ou_app', '{}', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	mustPass(t, "等值命中", checkPCContractExists(ctx, d, form,
		hcBody(nil, map[string]any{"contract_no": "CT-2609-0002"}), "ou_app"))
}

func TestPCChangeChecks(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["PC"]
	ctx := context.Background()

	mustBlock(t, "变更额为0", checkPCChangeNonzero(ctx, d, form, hcBody(nil, map[string]any{"change_amount_cents": 0}), "ou_app"))
	mustBlock(t, "变更额缺失", checkPCChangeNonzero(ctx, d, form, hcBody(nil, map[string]any{}), "ou_app"))
	mustPass(t, "变更额非0", checkPCChangeNonzero(ctx, d, form, hcBody(nil, map[string]any{"change_amount_cents": 50000}), "ou_app"))

	mustBlock(t, "超150%重走", checkPCResetBlock(ctx, d, form, hcBody(nil,
		map[string]any{"new_total_cents": 200000, "original_contract_amount_cents": 100000}), "ou_app"))
	mustPass(t, "150%内放行", checkPCResetBlock(ctx, d, form, hcBody(nil,
		map[string]any{"new_total_cents": 140000, "original_contract_amount_cents": 100000}), "ou_app"))

	mustBlock(t, "超30%缺说明", checkPCSpecialExplanation(ctx, d, form, hcBody(nil,
		map[string]any{"total_change_cents": 40000, "original_contract_amount_cents": 100000}), "ou_app"))
	mustPass(t, "超30%有说明", checkPCSpecialExplanation(ctx, d, form, hcBody(nil,
		map[string]any{"total_change_cents": 40000, "original_contract_amount_cents": 100000,
			"special_explanation": "原材料涨价"}), "ou_app"))
	mustPass(t, "30%内不拦", checkPCSpecialExplanation(ctx, d, form, hcBody(nil,
		map[string]any{"total_change_cents": 20000, "original_contract_amount_cents": 100000}), "ou_app"))

	mustBlock(t, "工程类缺技评", checkPCTechOpinionEngineering(ctx, d, form, hcBody(nil,
		map[string]any{"is_engineering_category": true}), "ou_app"))
	mustPass(t, "工程类带技评", checkPCTechOpinionEngineering(ctx, d, form, hcBody(nil,
		map[string]any{"is_engineering_category": true, "tech_opinion": "可行", "tech_opinion_by": "质检"}), "ou_app"))
	mustPass(t, "非工程类不拦", checkPCTechOpinionEngineering(ctx, d, form, hcBody(nil,
		map[string]any{"is_engineering_category": false}), "ou_app"))

	mustBlock(t, "换供应商同名", checkPCNewSupplier(ctx, d, form, hcBody(nil,
		map[string]any{"change_type": "供应商变更", "new_supplier": "甲供", "original_supplier": "甲供"}), "ou_app"))
	mustBlock(t, "换供应商未填", checkPCNewSupplier(ctx, d, form, hcBody(nil,
		map[string]any{"change_type": "供应商变更", "original_supplier": "甲供"}), "ou_app"))
	mustPass(t, "换供应商异名", checkPCNewSupplier(ctx, d, form, hcBody(nil,
		map[string]any{"change_type": "供应商变更", "new_supplier": "乙供", "original_supplier": "甲供"}), "ou_app"))
	mustPass(t, "非换供应商", checkPCNewSupplier(ctx, d, form, hcBody(nil,
		map[string]any{"change_type": "价格调整", "original_supplier": "甲供"}), "ou_app"))
}

// ---------------- 未注册 hard 判据 fail-closed ----------------

func TestUnknownHardCheckFailsClosed(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["CT"]
	// 只挂一条未知的提交时点 hard 判据 —— 必须可见失败，不许静默通过
	//（清空既有 checks 避免先命中其它判据，聚焦分派器的 fail-closed 行为）
	form.Checks = []specload.CheckDoc{{
		ID: "no_such_evaluator", When: "submit", Severity: "hard", Assert: "???",
	}}
	err := d.evaluateHardChecks(context.Background(), form, hcBody(nil, ctFullClauseFields()), "ou_app")
	mustBlock(t, "未注册hard判据", err)
	if err != nil && !strings.Contains(err.Error(), "未实现求值器") {
		t.Errorf("应提示注册求值器：%v", err)
	}
}

// ---------------- 算链不变量（chain 侧） ----------------

func TestPCChangeTierInvariants(t *testing.T) {
	// R-15 公式 + R-30 采一档不可达（合同前提：原合同 ≥1,000 元）
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatal(err)
	}
	// 锚定 spec 公式（防漂移 —— 直接查内嵌 chain.json 原文）
	raw, err := specfs.FS.ReadFile("spec/chain.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "max(change_amount_cents, original_contract_amount_cents)") {
		t.Fatal("chain.json#routes.change.rules.tier_formula 应含 max(change, original) 公式（R-15）—— spec 漂移")
	}
	for _, orig := range []int64{100000, 150000, 5000000} {
		for _, chg := range []int64{-50000, 1, 50000, 900000} {
			tier, err := chain.ChangeTierOf(b, chg, orig)
			if err != nil {
				t.Fatal(err)
			}
			if tier == "purchase_tier1" {
				t.Errorf("R-30 不变量破坏：orig=%d chg=%d 落入采一档", orig, chg)
			}
		}
	}
	// 就高公式本身：change < original ⇒ 取 original 侧
	tier, err := chain.ChangeTierOf(b, 100000, 600001)
	if err != nil {
		t.Fatal(err)
	}
	if tier != "purchase_tier3" {
		t.Errorf("max(100000,600001)=600001 应落采三档，实为 %s", tier)
	}
	// 400001 分＝4,000.01 元 ⇒ 采二（顺带钉住「分为单位」换算）
	if tier2, err := chain.ChangeTierOf(b, 100000, 400001); err != nil || tier2 != "purchase_tier2" {
		t.Errorf("max(100000,400001)=400001 应落采二档，实为 %s（err=%v）", tier2, err)
	}
}
