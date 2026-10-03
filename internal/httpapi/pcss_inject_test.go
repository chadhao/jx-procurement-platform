package httpapi

// N-036 生产者侧验收：PC/SS 提交期系统字段注入 ＋ PC#resubmit_to_group_at 的既有承载。
//   ① PC：original ← L04.amount · related ← L04.ext · tier ← R-15 就高 ·
//      count/cumulative/anomaly/change_chain ← L09 历史聚合（含本次）· exception_type；
//   ② SS：exception_type；③ L04 等值查不到 ⇒ fail-closed（可见失败，不静默注入垃圾）；
//   ④ PC 缺 resubmitted_to_group_at ⇒ 提交期结构化必填拦（M4 既有承载 —— pending_implementation 的更正证据）。

import (
	"context"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

func TestInjectPCSSSystemFields(t *testing.T) {
	d := Deps{DB: hcDB(t), Spec: metaTestBundle(t)}
	ctx := context.Background()

	// seed：L04 合同行（biz_no=合同号 · amount=50 万分 · ext.related_biz_no=PR）
	if _, err := d.DB.ExecContext(ctx, `
INSERT INTO t_ledger_archive(ledger_type,biz_no,instance_code,source_doc_type,department,applicant_open_id,
  amount_cents,ext_json,biz_date,created_at,updated_at)
VALUES('L04','CT-2609-0001','I-CT-1','CT','生产部','ou_a',500000,
  '{"related_biz_no":"PR-2609-0001","contract_no":"CT-2609-0001"}','2026-10-01','2026-10-01','2026-10-01')`); err != nil {
		t.Fatal(err)
	}
	// seed：L09 历史变更一行（同合同 · 变更 20 万分 · 近期 → 90 天内）
	if _, err := d.DB.ExecContext(ctx, `
INSERT INTO t_ledger_archive(ledger_type,biz_no,instance_code,source_doc_type,department,applicant_open_id,
  amount_cents,ext_json,biz_date,created_at,updated_at)
VALUES('L09','PC-2609-0001','I-PC-1','PC','生产部','ou_a',200000,
  '{"contract_no":"CT-2609-0001","change_amount_cents":200000,"exception_type":"采购变更"}','2026-10-01','2026-10-01','2026-10-01')`); err != nil {
		t.Fatal(err)
	}

	// ① PC 注入
	body := &approvalSubmitBody{DocType: "PC", Fields: map[string]any{
		"contract_no":             "CT-2609-0001",
		"change_amount_cents":     float64(300000),
		"resubmitted_to_group_at": "2026-10-03",
	}}
	if err := d.injectPCSSSystemFields(ctx, body); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	f := body.Fields
	if f["original_contract_amount_cents"] != int64(500000) && f["original_contract_amount_cents"] != float64(500000) {
		t.Errorf("original = %v, 期望 500000（L04 带入不可手改）", f["original_contract_amount_cents"])
	}
	if f["related_biz_no"] != "PR-2609-0001" {
		t.Errorf("related_biz_no = %v, 期望由 contract_no 反查 L04.ext 得 PR-2609-0001", f["related_biz_no"])
	}
	// R-15 就高：max(300000,500000)=500000 ⇒ tier2
	if f["applicable_tier"] != "purchase_tier2" {
		t.Errorf("applicable_tier = %v, 期望 purchase_tier2（max(变更,原合同)=500000）", f["applicable_tier"])
	}
	if c, _ := f["change_count_to_date"].(int); c != 2 {
		t.Errorf("change_count_to_date = %v, 期望 2（历史 1 + 本次含本次）", f["change_count_to_date"])
	}
	if v, _ := f["is_anomaly_listed"].(bool); !v {
		t.Errorf("is_anomaly_listed = %v, 期望 true（90 天内已有 1 次历史 ⇒ 含本次 ≥2）", f["is_anomaly_listed"])
	}
	if f["exception_type"] != "采购变更" {
		t.Errorf("exception_type = %v", f["exception_type"])
	}
	cc, _ := f["change_chain"].(map[string]any)
	if cc == nil || cc["tier"] != "purchase_tier2" {
		t.Errorf("change_chain = %v（R-21#7 三要素缺 tier/结构）", f["change_chain"])
	}
	if v, ok := cc["cumulative_change_cents"].(int64); !ok || v != 500000 {
		t.Errorf("cumulative = %v, 期望 500000（历史 20 万 + 本次 30 万）", cc["cumulative_change_cents"])
	}

	// ② SS 注入
	ss := &approvalSubmitBody{DocType: "SS", Fields: map[string]any{}}
	if err := d.injectPCSSSystemFields(ctx, ss); err != nil {
		t.Fatal(err)
	}
	if ss.Fields["exception_type"] != "独家采购" {
		t.Errorf("SS exception_type = %v, 期望 独家采购", ss.Fields["exception_type"])
	}

	// ③ L04 查不到 ⇒ fail-closed（不静默注入）
	bad := &approvalSubmitBody{DocType: "PC", Fields: map[string]any{
		"contract_no": "CT-0000-0000", "change_amount_cents": float64(1),
	}}
	if err := d.injectPCSSSystemFields(ctx, bad); err == nil {
		t.Fatal("L04 无等值合同应 fail-closed（可见失败），不可静默注入垃圾 original")
	}

	// ④ ★ N-038 反转：服务端权威**恒覆盖**（原「不覆盖非空」被 WB 变异探针打穿 ——
	//    applicable_tier 可降档、伪造类型可残留 ⇒ 测试断言与注释一并反转）。
	forged := &approvalSubmitBody{DocType: "PC", Fields: map[string]any{
		"contract_no":                    "CT-2609-0001",
		"change_amount_cents":            float64(300000),
		"applicable_tier":                "purchase_tier1", // 伪造降档
		"original_contract_amount_cents": float64(1),       // 伪造金额
		"change_count_to_date":           float64(1),
		"is_anomaly_listed":              false,
		"exception_type":                 "伪造类型",
	}}
	if err := d.injectPCSSSystemFields(ctx, forged); err != nil {
		t.Fatal(err)
	}
	gf := forged.Fields
	if gf["applicable_tier"] == "purchase_tier1" {
		t.Error("伪造降档未被服务端权威覆盖 —— putSysField 又退回「不覆盖」了（N-038 ①）")
	}
	if gf["applicable_tier"] != "purchase_tier2" {
		t.Errorf("applicable_tier = %v, 期望服务端计算值 purchase_tier2", gf["applicable_tier"])
	}
	if gf["exception_type"] != "采购变更" {
		t.Errorf("exception_type = %v, 期望服务端权威值 采购变更", gf["exception_type"])
	}
	if gf["is_anomaly_listed"] != true {
		t.Errorf("is_anomaly_listed = %v, 期望服务端计算值 true", gf["is_anomaly_listed"])
	}

	// ⑤ N-038 附一（已定口径 5 项）断言
	f5 := forged.Fields
	if f5["total_change_cents"] != int64(500000) {
		t.Errorf("total_change_cents = %v, 期望 500000（历史 20 万＋本次 30 万）", f5["total_change_cents"])
	}
	if f5["new_total_cents"] != int64(1000000) {
		t.Errorf("new_total_cents = %v, 期望 1000000（原 50 万＋累计 50 万）", f5["new_total_cents"])
	}
	// last_change_at：无历史才空 —— 本例有历史（2026-10-01）
	if v, _ := f5["last_change_at"].(string); v == "" {
		t.Errorf("last_change_at 应取上次变更日期，实为空: %v", f5["last_change_at"])
	}
	// is_reset: newTotal 1000000 vs 500000*1.5=750000 ⇒ true
	if v, _ := f5["is_reset_as_new_purchase"].(bool); !v {
		t.Errorf("is_reset_as_new_purchase = %v, 期望 true（100 万 > 75 万）", f5["is_reset_as_new_purchase"])
	}
	// is_engineering_category：L04 ext 无 usage ⇒ 不写（不伪造）；补 ext 用例
	if _, has := f5["is_engineering_category"]; has {
		t.Errorf("L04.ext 无 usage_category_l1 时不伪造 engineering 标记: %v", f5["is_engineering_category"])
	}
	if _, err := d.DB.ExecContext(ctx,
		`UPDATE t_ledger_archive SET ext_json = '{"related_biz_no":"PR-2609-0001","usage_category_l1":"P06"}'
WHERE ledger_type='L04' AND biz_no='CT-2609-0001'`); err != nil {
		t.Fatal(err)
	}
	f2 := &approvalSubmitBody{DocType: "PC", Fields: map[string]any{
		"contract_no": "CT-2609-0001", "change_amount_cents": float64(300000),
	}}
	if err := d.injectPCSSSystemFields(ctx, f2); err != nil {
		t.Fatal(err)
	}
	if v, _ := f2.Fields["is_engineering_category"].(bool); !v {
		t.Errorf("usage=P06 ⇒ is_engineering_category 应 true, 实为 %v", f2.Fields["is_engineering_category"])
	}
}

// TestPCResubmitNotAtSubmitStage C 的更正认知：PC#resubmitted_to_group_at 的
// section `approval_and_filing.filled_at="node3_approval_and_node4_ledger"` ≠ 提交时点
// ⇒ **提交期正确行为＝跳过**（不是 M4 必填）；必填承载＝**节点时点**
// （flow#nodeFieldSpecFor：PC×ledger_submit —— rule 登记人＝综合运营主管＝该节点 actor）。
func TestPCResubmitNotAtSubmitStage(t *testing.T) {
	form := metaTestBundle(t).Forms["PC"]
	if form.DocType == "" {
		t.Fatal("缺 PC 表单")
	}
	missing := map[string]any{
		"contract_no": "CT-2609-0001", "change_type": "价格变更",
		"change_amount_cents": float64(10000), "change_reason": "原料涨价",
		// resubmitted_to_group_at 缺 —— 属 node3/node4 时点，提交期不该拦
	}
	if err := validateSubmitForm(form, missing); err != nil {
		t.Fatalf("filled_at≠提交时点的字段不应在提交期拦（错拦=把登记动作逼进提交瞬间）: %v", err)
	}
}

var _ = store.DB{}
