package httpapi

// QC 判据验收：4 条提交时点 hard（各拦+放）＋ 提交后 L07 写入（成功/行缺失可见失败）。

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func qcBody(fields map[string]any) *approvalSubmitBody {
	return &approvalSubmitBody{DocType: "QC", Fields: fields}
}

func TestQCRelatedGRExists(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["QC"]
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_instance (instance_code, approval_code, doc_type, biz_no, status,
  applicant_open_id, amount_cents, source, created_at, updated_at)
VALUES ('I-GR-1','ac-gr-001','GR','GR-2609-0001','APPROVED','ou_app',1000,'flow',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}

	mustBlock(t, "缺关联", checkQCRelatedGRExists(ctx, d, form, qcBody(map[string]any{}), "ou_app"))
	mustBlock(t, "关联不存在", checkQCRelatedGRExists(ctx, d, form,
		qcBody(map[string]any{"related_biz_no": "GR-0000-0000"}), "ou_app"))
	mustPass(t, "等值命中GR", checkQCRelatedGRExists(ctx, d, form,
		qcBody(map[string]any{"related_biz_no": "GR-2609-0001"}), "ou_app"))
}

func TestQCInspectionResult(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["QC"]
	mustBlock(t, "结论空", checkQCInspectionResult(context.Background(), d, form, qcBody(map[string]any{}), "ou_app"))
	mustBlock(t, "结论越界", checkQCInspectionResult(context.Background(), d, form,
		qcBody(map[string]any{"inspection_result": "还行"}), "ou_app"))
	for _, ok := range []string{"合格", "不合格", "让步使用"} {
		mustPass(t, "结论"+ok, checkQCInspectionResult(context.Background(), d, form,
			qcBody(map[string]any{"inspection_result": ok}), "ou_app"))
	}
}

func TestQCSampleQuantityPair(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["QC"]
	mustBlock(t, "抽检缺数量", checkQCSampleQuantityPair(context.Background(), d, form,
		qcBody(map[string]any{"inspection_method": "抽检"}), "ou_app"))
	mustBlock(t, "抽检数量0", checkQCSampleQuantityPair(context.Background(), d, form,
		qcBody(map[string]any{"inspection_method": "抽检", "sample_quantity": 0}), "ou_app"))
	mustPass(t, "抽检带数量", checkQCSampleQuantityPair(context.Background(), d, form,
		qcBody(map[string]any{"inspection_method": "抽检", "sample_quantity": 10}), "ou_app"))
	mustPass(t, "全检无数量", checkQCSampleQuantityPair(context.Background(), d, form,
		qcBody(map[string]any{"inspection_method": "全检"}), "ou_app"))
	mustBlock(t, "全检却填数量", checkQCSampleQuantityPair(context.Background(), d, form,
		qcBody(map[string]any{"inspection_method": "全检", "sample_quantity": 5}), "ou_app"))
}

func TestQCDefectDescription(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["QC"]
	mustPass(t, "合格无须描述", checkQCDefectDescription(context.Background(), d, form,
		qcBody(map[string]any{"inspection_result": "合格"}), "ou_app"))
	mustPass(t, "结论空另由必填拦", checkQCDefectDescription(context.Background(), d, form,
		qcBody(map[string]any{}), "ou_app"))
	mustBlock(t, "不合格缺描述", checkQCDefectDescription(context.Background(), d, form,
		qcBody(map[string]any{"inspection_result": "不合格"}), "ou_app"))
	mustPass(t, "不合格带描述", checkQCDefectDescription(context.Background(), d, form,
		qcBody(map[string]any{"inspection_result": "不合格", "defect_description": "色差超标"}), "ou_app"))
	mustBlock(t, "让步使用缺描述", checkQCDefectDescription(context.Background(), d, form,
		qcBody(map[string]any{"inspection_result": "让步使用"}), "ou_app"))
}

func TestQCPostSubmitL07Write(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// 关联 GR 的 L07 行（QC producer 写入 GR 行 —— ledger-mapping#L07.producer=[GR,QC]）
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_ledger_archive (ledger_type, biz_no, instance_code, source_doc_type, department,
  applicant_open_id, ext_json, created_at, updated_at)
VALUES ('L07','GR-2609-0001','I-GR-1','GR','质检技术部','ou_app','{"acceptance_conclusion":"合格"}',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}

	// 放：行存在 ⇒ 写入 inspection_conclusion（JSON 键级合并，原键保留）
	if err := d.verifyQCPostSubmitL07(ctx,
		qcBody(map[string]any{"related_biz_no": "GR-2609-0001", "inspection_result": "不合格"}),
		"QC-2609-0001"); err != nil {
		t.Fatalf("L07 写入失败: %v", err)
	}
	var raw string
	if err := db.QueryRowContext(ctx,
		`SELECT ext_json FROM t_ledger_archive WHERE ledger_type='L07' AND biz_no='GR-2609-0001'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ext map[string]any
	if err := json.Unmarshal([]byte(raw), &ext); err != nil {
		t.Fatal(err)
	}
	if ext["inspection_conclusion"] != "不合格" {
		t.Errorf("inspection_conclusion = %v", ext["inspection_conclusion"])
	}
	if ext["acceptance_conclusion"] != "合格" {
		t.Errorf("既有键被破坏: %v", ext) // 键级合并不得整体覆盖
	}

	// 拦：L07 行不存在 ⇒ 可见失败（不静默）
	err := d.verifyQCPostSubmitL07(ctx,
		qcBody(map[string]any{"related_biz_no": "GR-9999-0009", "inspection_result": "合格"}),
		"QC-2609-0002")
	if err == nil {
		t.Error("L07 行缺失必须可见失败（「没有数据」≠「没有违规」）")
	}
}
