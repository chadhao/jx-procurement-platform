package httpapi

// N-062 J2：soft 判据提示通道的可机检判据。
// ① 通道 e2e（RFQ：执行 + 200 不阻断 + data.warnings 可见；QC：重复批提示）
// ② 单评器直调（GR 关联冲突 / SA 对外支付 / SS 固定资产 / SUB 超期 —— 各正反两例）
// ③ 未注册 soft ⇒ 响亮可见但不阻断。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func warnIDs(ws []SoftWarning) string {
	var ids []string
	for _, w := range ws {
		ids = append(ids, w.ID)
	}
	return strings.Join(ids, ",")
}

func hasWarn(ws []SoftWarning, id string) bool {
	for _, w := range ws {
		if w.ID == id {
			return true
		}
	}
	return false
}

// TestSoftChannelRFQWarningsE2E 通道端到端：responded_count=1 ⇒ 200（不阻断）且
// data.warnings 带 response_shortfall_warning；=3 ⇒ 成功响应无 warnings 键。
func TestSoftChannelRFQWarningsE2E(t *testing.T) {
	e, _, auth, _ := newA8SubmitApp(t, nil)
	cookie := auth.Establish("ou_app")

	// 正例：不足 3 家 ⇒ 提示
	p1 := strings.Replace(a8RFQPayload(), `"responded_count":3`, `"responded_count":1`, 1)
	if p1 == a8RFQPayload() {
		t.Fatal("responded_count 替换未生效（载荷形状变了？）")
	}
	code, env := postSubmit(t, e, cookie, p1, "")
	if code != 200 {
		t.Fatalf("soft 命中不得阻断：应 200，实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	wsRaw, ok := d["warnings"].([]any)
	if !ok {
		t.Fatalf("成功响应缺 data.warnings（提示不可见）: %v", d)
	}
	found := false
	for _, w := range wsRaw {
		m, _ := w.(map[string]any)
		if m["id"] == "response_shortfall_warning" {
			found = true
			if !strings.Contains(m["message"].(string), "不足 3 家") {
				t.Errorf("提示文案 = %v", m["message"])
			}
		}
	}
	if !found {
		t.Fatalf("warnings 未含 response_shortfall_warning: %v", wsRaw)
	}

	// 反例：3 家 ⇒ 无 warnings 键（载荷形状零变化）
	code2, env2 := postSubmit(t, e, cookie, a8RFQPayload(), "")
	if code2 != 200 {
		t.Fatalf("应 200, 实为 %d（%s）", code2, env2.Message)
	}
	d2, _ := env2.Data.(map[string]any)
	if _, has := d2["warnings"]; has {
		t.Errorf("无提示时不应带 warnings 键: %v", d2)
	}
}

// TestSoftQCDuplicateE2E 同 related＋同 batch 重复录入 ⇒ 第二单 200 且带提示。
func TestSoftQCDuplicateE2E(t *testing.T) {
	e, _, auth, _ := newA8SubmitApp(t, nil)
	cookie := auth.Establish("ou_app")
	// 首单：无先例 ⇒ 无提示
	code, env := postSubmit(t, e, cookie, a8QCPayload(), "")
	if code != 200 {
		t.Fatalf("首单应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	if _, has := d["warnings"]; has {
		t.Errorf("首单不应提示: %v", d)
	}
	// 二单：同 related＋同 batch（两单批号皆空，按字面等值）⇒ 提示且仍 200
	code2, env2 := postSubmit(t, e, cookie, a8QCPayload(), "")
	if code2 != 200 {
		t.Fatalf("重复 QC 不得阻断（一对多是设计）：应 200，实为 %d（%s）", code2, env2.Message)
	}
	d2, _ := env2.Data.(map[string]any)
	wsRaw, _ := d2["warnings"].([]any)
	found := false
	for _, w := range wsRaw {
		if m, _ := w.(map[string]any); m["id"] == "no_duplicate_qc_for_same_batch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings 未含 no_duplicate_qc_for_same_batch: %v", d2)
	}
}

// TestSoftGRInspectionHint GR 关联冲突提示（直调 —— QC 依附已提交 GR ⇒ 重提面）。
func TestSoftGRInspectionHint(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db, Spec: metaTestBundle(t)}
	ctx := context.Background()
	// 种子：指向 GR-2610-0999 的不合格 QC
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_instance(instance_code, approval_code, doc_type, biz_no, status,
  applicant_open_id, department, purpose_class_l1, purpose_class_l2, supplier,
  source, created_at, updated_at, ext_json)
VALUES('QCSEED','code-qc','QC','QC-2610-0991','APPROVED','ou_qc','质检部','','','',
  'event',datetime('now'),datetime('now'),
  '{"related_biz_no":"GR-2610-0999","inspection_result":"不合格","batch_no":"B1"}')`); err != nil {
		t.Fatal(err)
	}
	form := d.Spec.Forms["GR"]

	// 正例：重提 GR＋合格入库 ⇒ 提示带 QC 单号
	b1 := grBody(map[string]any{"acceptance_conclusion": "合格入库"})
	b1.PrevBizNo = "GR-2610-0999"
	ws1 := d.evaluateSoftChecks(ctx, form, b1)
	if !hasWarn(ws1, "inspection_vs_conclusion_hint") {
		t.Fatalf("应提示 inspection_vs_conclusion_hint: %s", warnIDs(ws1))
	}
	for _, w := range ws1 {
		if w.ID == "inspection_vs_conclusion_hint" && !strings.Contains(w.Message, "QC-2610-0991") {
			t.Errorf("提示应带 QC 单号: %q", w.Message)
		}
	}

	// 反例 A：结论非「合格入库」⇒ 不提示
	b2 := grBody(map[string]any{"acceptance_conclusion": "退货"})
	b2.PrevBizNo = "GR-2610-0999"
	if hasWarn(d.evaluateSoftChecks(ctx, form, b2), "inspection_vs_conclusion_hint") {
		t.Error("退货结论不应提示")
	}

	// 反例 B：首提（无 PrevBizNo）⇒ 天然跳过（不误报）
	b3 := grBody(map[string]any{"acceptance_conclusion": "合格入库"})
	if hasWarn(d.evaluateSoftChecks(ctx, form, b3), "inspection_vs_conclusion_hint") {
		t.Error("首提无前置 QC 可查，不应提示")
	}
}

// TestSoftSACounterparty SA 对外支付缺交易对手（「需要开票」分支无字段 ⇒ 不实现，见头注）。
func TestSoftSACounterparty(t *testing.T) {
	d := Deps{Spec: metaTestBundle(t)}
	form := d.Spec.Forms["SA"]
	ctx := context.Background()

	hit := &approvalSubmitBody{DocType: "SA", PaymentMethodInput: "对公直付",
		Fields: map[string]any{}}
	if !hasWarn(d.evaluateSoftChecks(ctx, form, hit), "counterparty_conditional") {
		t.Error("对公直付且对方单位空 ⇒ 应提示")
	}
	filled := &approvalSubmitBody{DocType: "SA", PaymentMethodInput: "对公直付",
		Fields: map[string]any{"counterparty": "湖南甲公司"}}
	if hasWarn(d.evaluateSoftChecks(ctx, form, filled), "counterparty_conditional") {
		t.Error("对方单位已填不应提示")
	}
	advance := &approvalSubmitBody{DocType: "SA", PaymentMethodInput: "垫付",
		Fields: map[string]any{}}
	if hasWarn(d.evaluateSoftChecks(ctx, form, advance), "counterparty_conditional") {
		t.Error("垫付（非对外支付）不应提示")
	}
}

// TestSoftSSFixedAssetConflict 固定资产 ∧ >20 万 同时成立才提示（严格按 assert）。
func TestSoftSSFixedAssetConflict(t *testing.T) {
	d := Deps{Spec: metaTestBundle(t)}
	form := d.Spec.Forms["SS"]
	ctx := context.Background()
	big := int64(20000001)
	small := int64(19999999)

	hit := &approvalSubmitBody{DocType: "SS", AmountCents: &big,
		Fields: map[string]any{"is_fixed_asset": true}}
	if !hasWarn(d.evaluateSoftChecks(ctx, form, hit), "fixed_asset_conflict") {
		t.Error("固定资产且 >20 万 ⇒ 应提示")
	}
	noFixed := &approvalSubmitBody{DocType: "SS", AmountCents: &big,
		Fields: map[string]any{"is_fixed_asset": false}}
	if hasWarn(d.evaluateSoftChecks(ctx, form, noFixed), "fixed_asset_conflict") {
		t.Error("非固定资产不应提示（assert 是 AND）")
	}
	low := &approvalSubmitBody{DocType: "SS", AmountCents: &small,
		Fields: map[string]any{"is_fixed_asset": true}}
	if hasWarn(d.evaluateSoftChecks(ctx, form, low), "fixed_asset_conflict") {
		t.Error("≤20 万不应提示（assert 是 AND）")
	}
}

// TestSoftSUBDeadline SUB 超期预警（阈值取 spec deadline_workdays；绝不阻断）。
func TestSoftSUBDeadline(t *testing.T) {
	d := Deps{Spec: metaTestBundle(t)}
	form := d.Spec.Forms["SUB"]
	ctx := context.Background()

	// 正例：湖南侧完成日远早于今天（超 3 工作日）
	old := subBody(map[string]any{"hunan_completed_at": "2026-09-01"})
	ws := d.evaluateSoftChecks(ctx, form, old)
	if !hasWarn(ws, "submit_deadline_warning") {
		t.Fatalf("超 3 工作日应提示: %s", warnIDs(ws))
	}
	for _, w := range ws {
		if w.ID == "submit_deadline_warning" && !strings.Contains(w.Message, "3 个工作日") {
			t.Errorf("提示应带 spec 阈值 3: %q", w.Message)
		}
	}

	// 反例 A：今日完成 ⇒ 未超期
	today := subBody(map[string]any{"hunan_completed_at": time.Now().Format("2006-01-02")})
	if hasWarn(d.evaluateSoftChecks(ctx, form, today), "submit_deadline_warning") {
		t.Error("当日完成不应提示")
	}

	// 反例 B：缺基线 ⇒ 跳过（不误报）
	none := subBody(map[string]any{})
	if hasWarn(d.evaluateSoftChecks(ctx, form, none), "submit_deadline_warning") {
		t.Error("缺 hunan_completed_at 不应提示")
	}
}

// TestSoftUnregisteredFailVisible 未注册 soft ⇒ 出 warning（响亮）且无 error 语义（不阻断）。
func TestSoftUnregisteredFailVisible(t *testing.T) {
	d := Deps{}
	form := specload.FormDoc{Checks: []specload.CheckDoc{
		{ID: "ghost_soft_check", Severity: "soft", When: "submit"},
		{ID: "hard_one", Severity: "hard", When: "submit"}, // hard 不进 soft 通道
	}}
	ws := d.evaluateSoftChecks(context.Background(), form, &approvalSubmitBody{DocType: "BA"})
	if len(ws) != 1 || ws[0].ID != "ghost_soft_check" {
		t.Fatalf("未注册 soft 应产出 1 条点名 warning，实为 %v", ws)
	}
}
