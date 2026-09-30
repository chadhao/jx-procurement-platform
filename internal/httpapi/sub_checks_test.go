package httpapi

// SUB 判据验收：5 条提交时点 hard（各拦+放）＋ soft 不阻断 ＋ 落账后 6+2 列自检。

import (
	"context"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func subBody(fields map[string]any) *approvalSubmitBody {
	return &approvalSubmitBody{DocType: "SUB", Fields: fields}
}

func TestSUBRelatedDocsComplete(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["SUB"]
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seedInst := func(biz, doc string) {
		if _, err := db.ExecContext(ctx, `
INSERT INTO t_instance (instance_code, approval_code, doc_type, biz_no, status,
  applicant_open_id, amount_cents, source, created_at, updated_at)
VALUES (?, 'ac-x', ?, ?, 'APPROVED', 'ou_app', 1000, 'flow', ?, ?)`,
			"I-"+biz, doc, biz, now, now); err != nil {
			t.Fatal(err)
		}
	}
	seedInst("CT-2609-0001", "CT")
	seedInst("GR-2609-0002", "GR")

	mustBlock(t, "清单空", checkSUBRelatedDocsComplete(ctx, d, form, subBody(map[string]any{}), "ou_app"))
	mustBlock(t, "写已齐但无单号", checkSUBRelatedDocsComplete(ctx, d, form,
		subBody(map[string]any{"related_docs": "材料已齐全"}), "ou_app"))
	mustBlock(t, "缺一项", checkSUBRelatedDocsComplete(ctx, d, form,
		subBody(map[string]any{"related_docs": "CT-2609-0001、GR-0000-0000"}), "ou_app"))
	mustPass(t, "逐项齐全", checkSUBRelatedDocsComplete(ctx, d, form,
		subBody(map[string]any{"related_docs": "CT-2609-0001、GR-2609-0002"}), "ou_app"))
}

func TestSUBContractApprovedThreshold(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["SUB"]
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_instance (instance_code, approval_code, doc_type, biz_no, status,
  applicant_open_id, amount_cents, source, created_at, updated_at)
VALUES ('I-CT-1','ac-x','CT','CT-2609-0001','APPROVED','ou_app',200000,'flow',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}

	// ★ 边界：恰好 100,000 分（1,000 元）＝含 → 适用（写成严格大于会漏拦）；
	// 关联清单里**没有**已批 CT ⇒ 拦（有已批 CT 的放行用例见下）
	mustBlock(t, "恰1000元无合同审批_拦", checkSUBContractApproved(ctx, d, form,
		subBody(map[string]any{"amount_cents": float64(100000),
			"related_docs": "GR-2609-0002"}), "ou_app"))
	mustPass(t, "999元不适用", checkSUBContractApproved(ctx, d, form,
		subBody(map[string]any{"amount_cents": float64(99999), "related_docs": "CT-2609-0001"}), "ou_app"))
	mustPass(t, "系统标记已批", checkSUBContractApproved(ctx, d, form,
		subBody(map[string]any{"amount_cents": float64(150000),
			"related_docs": "CT-2609-0001", "contract_approved": true}), "ou_app"))
	mustPass(t, "清单内CT已批", checkSUBContractApproved(ctx, d, form,
		subBody(map[string]any{"amount_cents": float64(150000),
			"related_docs": "CT-2609-0001", "contract_approved": false}), "ou_app"))
	mustBlock(t, "无已批合同_拦", checkSUBContractApproved(ctx, d, form,
		subBody(map[string]any{"amount_cents": float64(150000),
			"related_docs": "GR-2609-0002", "contract_approved": false}), "ou_app"))
}

func TestSUBPayeeChangeCallback(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["SUB"]
	mustPass(t, "变更回拨齐", checkSUBPayeeChangeCallback(context.Background(), d, form,
		subBody(map[string]any{"payee_account_verified": false,
			"callback_confirmed": true, "callback_note": "已致电财务复核"}), "ou_app"))
	mustBlock(t, "变更未回拨", checkSUBPayeeChangeCallback(context.Background(), d, form,
		subBody(map[string]any{"payee_account_verified": false}), "ou_app"))
	mustBlock(t, "回拨无备注", checkSUBPayeeChangeCallback(context.Background(), d, form,
		subBody(map[string]any{"payee_account_verified": false, "callback_confirmed": true}), "ou_app"))
	mustPass(t, "一致无回拨", checkSUBPayeeChangeCallback(context.Background(), d, form,
		subBody(map[string]any{"payee_account_verified": true}), "ou_app"))
	mustBlock(t, "一致却填回拨", checkSUBPayeeChangeCallback(context.Background(), d, form,
		subBody(map[string]any{"payee_account_verified": true,
			"callback_confirmed": true, "callback_note": "多余"}), "ou_app"))
}

func TestSUBToleranceNote(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["SUB"]
	mustBlock(t, "结论缺", checkSUBToleranceNote(context.Background(), d, form,
		subBody(map[string]any{}), "ou_app"))
	mustPass(t, "超容差带说明", checkSUBToleranceNote(context.Background(), d, form,
		subBody(map[string]any{"three_way_match": "差异超容差", "tolerance_note": "整机价差 3%"}), "ou_app"))
	mustBlock(t, "超容差缺说明", checkSUBToleranceNote(context.Background(), d, form,
		subBody(map[string]any{"three_way_match": "差异超容差"}), "ou_app"))
	mustPass(t, "一致无说明", checkSUBToleranceNote(context.Background(), d, form,
		subBody(map[string]any{"three_way_match": "一致"}), "ou_app"))
	mustBlock(t, "一致却带说明", checkSUBToleranceNote(context.Background(), d, form,
		subBody(map[string]any{"three_way_match": "一致", "tolerance_note": "多余"}), "ou_app"))
}

func TestSUBHandoverReceiptRequired(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["SUB"]
	mustBlock(t, "无凭证", checkSUBHandoverReceipt(context.Background(), d, form,
		subBody(map[string]any{}), "ou_app"))
	mustPass(t, "有凭证", checkSUBHandoverReceipt(context.Background(), d, form,
		subBody(map[string]any{"handover_receipt": "att-1"}), "ou_app"))
}

// TestSUBSoftDeadlineNotExecuted soft 判据不进硬执行（超期只预警不阻断 ——
// 拒绝会把「交晚了」逼成「改日期」）。
func TestSUBSoftDeadlineNotExecuted(t *testing.T) {
	d := Deps{}
	softOnly := specload.FormDoc{DocType: "SUB", Checks: []specload.CheckDoc{
		{ID: "submit_deadline_warning", When: "submit", Severity: "soft", Assert: "超3工作日提示"},
	}}
	if err := d.evaluateHardChecks(context.Background(), softOnly, subBody(map[string]any{}), "ou_app"); err != nil {
		t.Errorf("soft 判据不得阻断提交：%v", err)
	}
}

func TestSUBPostLedgerL06SelfCheck(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seed := func(biz, archiveExt string, withOps bool, opsJSON string) {
		if _, err := db.ExecContext(ctx, `
INSERT INTO t_ledger_archive (ledger_type, biz_no, instance_code, source_doc_type, department,
  applicant_open_id, amount_cents, ext_json, created_at, updated_at)
VALUES ('L06', ?, ?, 'SUB', '综合运营部', 'ou_app', 50000, ?, ?, ?)`,
			biz, "I-"+biz, archiveExt, now, now); err != nil {
			t.Fatal(err)
		}
		if withOps {
			if _, err := db.ExecContext(ctx, `
INSERT INTO t_ledger_ops (ledger_type, biz_no, ops_json, updated_at)
VALUES ('L06', ?, ?, ?)`, biz, opsJSON, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	fullArchive := `{"related_docs":"CT-2609-0001","item_type":"货物","payment_method":"公户转账",` +
		`"hunan_completed_at":"2026-10-01"}`
	fullOps := `{"submit_group_at":"2026-10-01","handover_receipt":"att-9"}`

	seed("SUB-2609-0001", fullArchive, true, fullOps)
	mustPass(t, "存档6列+运营2列齐", d.verifySUBPostLedgerL06(ctx, "SUB-2609-0001"))

	seed("SUB-2609-0002", `{"related_docs":"CT-1","item_type":"货物","payment_method":"公户转账"}`, true, fullOps)
	mustBlock(t, "缺hunan列", d.verifySUBPostLedgerL06(ctx, "SUB-2609-0002"))

	seed("SUB-2609-0003", fullArchive, false, "")
	mustBlock(t, "缺运营行", d.verifySUBPostLedgerL06(ctx, "SUB-2609-0003"))

	mustBlock(t, "行不存在", d.verifySUBPostLedgerL06(ctx, "SUB-0000-0000"))
}
