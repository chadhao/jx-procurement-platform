package httpapi

// RFQ 判据验收：5 条提交时点 hard（各拦+放）＋ soft 不阻断。
// ★ 易错点逐条固化：①门槛随档位（询比价≥3 / 直采≥1）②not_single_source 与
// BJ 同款共用 ③本单无落账自检，存在证明全在附件（send_evidence）。

import (
	"context"
	"testing"
)

func rfqBody(fields map[string]any) *approvalSubmitBody {
	return &approvalSubmitBody{DocType: "RFQ", Fields: fields}
}

// related：RFQ 提交时 PR 必然还在审批中（parent=PR@rfq）——PENDING 必须放行，
// 这是分流的关键用例；SS 口径（须 APPROVED）由同一函数按 DocType 保持。
func TestRFQRelatedPRMustExist(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["RFQ"]
	ctx := context.Background()
	seedRelatedInstance(t, db, "ac-pr-pending", "PR", "PR-2609-0010") // APPROVED
	// 造审批中的 PR（RFQ 的真实场景）
	if _, err := db.ExecContext(ctx, `
UPDATE t_instance SET status='PENDING' WHERE biz_no='PR-2609-0010'`); err != nil {
		t.Fatal(err)
	}
	seedRelatedInstance(t, db, "ac-ct", "CT", "CT-2609-0011")

	mustBlock(t, "空", checkRelatedPRMustExist(ctx, d, form,
		rfqBody(map[string]any{}), "ou_app"))
	mustBlock(t, "不存在", checkRelatedPRMustExist(ctx, d, form,
		rfqBody(map[string]any{"related_biz_no": "PR-0000-0000"}), "ou_app"))
	mustBlock(t, "非PR类型", checkRelatedPRMustExist(ctx, d, form,
		rfqBody(map[string]any{"related_biz_no": "CT-2609-0011"}), "ou_app"))
	// ★ PR 审批中也放行（RFQ 挂在 PR 链 seq2；查 APPROVED 会拦掉全部合法 RFQ）
	mustPass(t, "PR审批中放行", checkRelatedPRMustExist(ctx, d, form,
		rfqBody(map[string]any{"related_biz_no": "PR-2609-0010"}), "ou_app"))
	// SS 同名判据回归：仍须已批准
	ssBody := &approvalSubmitBody{DocType: "SS",
		Fields: map[string]any{"related_biz_no": "PR-2609-0010"}}
	mustBlock(t, "SS口径仍拦审批中", checkRelatedPRMustExist(ctx, d, form, ssBody, "ou_app"))
}

func TestRFQInvitedMinByTier(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["RFQ"]
	ctx := context.Background()
	detail3 := "甲公司 | 报价10000\n乙公司 | 报价10200\n丙公司 | 报价10500"
	detail1 := "甲公司 | 报价10000"

	// 询比价（采三档）⇒ ≥3
	mustBlock(t, "询比价明细2", checkRFQInvitedMinByTier(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "询比价", "invited_suppliers": "甲公司 | 10000\n乙公司 | 10200", "invited_count": 2}), "ou_app"))
	mustPass(t, "询比价明细3", checkRFQInvitedMinByTier(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "询比价", "invited_suppliers": detail3, "invited_count": 3}), "ou_app"))
	mustBlock(t, "手填3明细2", checkRFQInvitedMinByTier(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "询比价", "invited_suppliers": "甲公司 | 10000\n乙公司 | 10200", "invited_count": 3}), "ou_app"))

	// ★ 直采（采二档报价比选 R5）⇒ ≥1 —— 统一按 3 会把主力档位全部误拦
	mustPass(t, "直采1家", checkRFQInvitedMinByTier(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "直采", "invited_suppliers": detail1, "invited_count": 1}), "ou_app"))
	mustBlock(t, "直采0家", checkRFQInvitedMinByTier(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "直采", "invited_suppliers": " ", "invited_count": 0}), "ou_app"))

	// 单一来源：本条放行（not_single_source 专拦）
	mustPass(t, "单一来源本条放行", checkRFQInvitedMinByTier(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "单一来源"}), "ou_app"))
	// 未登记方式：fail-closed
	mustBlock(t, "招标fail-closed", checkRFQInvitedMinByTier(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "招标", "invited_suppliers": detail3, "invited_count": 3}), "ou_app"))
	// 明细空（询比价）
	mustBlock(t, "明细空", checkRFQInvitedMinByTier(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "询比价", "invited_count": 3}), "ou_app"))
}

// not_single_source 与 BJ 同款共用 —— RFQ 入口也要拦一次。
func TestRFQNotSingleSource(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["RFQ"]
	ctx := context.Background()
	mustBlock(t, "RFQ单一来源", checkBJNotSingleSource(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "单一来源"}), "ou_app"))
	mustPass(t, "RFQ询比价", checkBJNotSingleSource(ctx, d, form,
		rfqBody(map[string]any{"procure_method": "询比价"}), "ou_app"))
}

func TestRFQDeadlineAfterSend(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["RFQ"]
	ctx := context.Background()

	mustBlock(t, "截止早于发出", checkRFQDeadlineAfterSend(ctx, d, form,
		rfqBody(map[string]any{"quote_deadline": "2026-10-01T14:00", "send_date": "2026-10-02"}), "ou_app"))
	mustPass(t, "截止晚于发出", checkRFQDeadlineAfterSend(ctx, d, form,
		rfqBody(map[string]any{"quote_deadline": "2026-10-05T14:00", "send_date": "2026-10-02"}), "ou_app"))
	mustPass(t, "同日下午", checkRFQDeadlineAfterSend(ctx, d, form,
		rfqBody(map[string]any{"quote_deadline": "2026-10-02T14:00", "send_date": "2026-10-02"}), "ou_app"))
	mustBlock(t, "坏格式不放行", checkRFQDeadlineAfterSend(ctx, d, form,
		rfqBody(map[string]any{"quote_deadline": "倒填的日期", "send_date": "2026-10-02"}), "ou_app"))
	mustBlock(t, "缺字段", checkRFQDeadlineAfterSend(ctx, d, form,
		rfqBody(map[string]any{}), "ou_app"))
}

func TestRFQSendEvidence(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["RFQ"]
	ctx := context.Background()

	mustBlock(t, "双缺", checkRFQSendEvidence(ctx, d, form,
		rfqBody(map[string]any{}), "ou_app"))
	mustBlock(t, "缺发出证据", checkRFQSendEvidence(ctx, d, form,
		rfqBody(map[string]any{"rfq_file": "att://f1"}), "ou_app"))
	mustBlock(t, "空串不算留存", checkRFQSendEvidence(ctx, d, form,
		rfqBody(map[string]any{"rfq_file": "att://f1", "send_evidence": "  "}), "ou_app"))
	mustPass(t, "两项齐", checkRFQSendEvidence(ctx, d, form,
		rfqBody(map[string]any{"rfq_file": "att://f1", "send_evidence": "att://e1"}), "ou_app"))
}

// 全合规：5 hard 放行 ＋ soft 实际报价不足不阻断（severity 天然跳过）。
func TestRFQHardAllPass(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["RFQ"]
	ctx := context.Background()
	seedRelatedInstance(t, db, "ac-pr", "PR", "PR-2609-0020")
	if _, err := db.ExecContext(ctx, `UPDATE t_instance SET status='PENDING' WHERE biz_no='PR-2609-0020'`); err != nil {
		t.Fatal(err)
	}
	body := rfqBody(map[string]any{
		"related_biz_no":    "PR-2609-0020",
		"procure_method":    "询比价",
		"invited_suppliers": "甲公司 | 10000\n乙公司 | 10200\n丙公司 | 10500",
		"invited_count":     3,
		"quote_deadline":    "2026-10-05T14:00",
		"send_date":         "2026-10-02",
		"rfq_file":          "att://f",
		"send_evidence":     "att://e",
		// soft：实际只回 1 家 —— 只提示不阻断（错拦会逼人去凑 3 家）
		"responded_count": 1,
		"shortfall_note":  "",
	})
	if err := d.evaluateHardChecks(ctx, form, body, "ou_app"); err != nil {
		t.Fatalf("全合规应放行: %v", err)
	}
}
