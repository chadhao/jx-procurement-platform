package httpapi

// N-067②③ / N-069：节点记录字段端到端（T1-a 落库 · T1-b 附件绑定 · T1-c 消费面）。
// ★ 落库断言是**真断言**（不只 200/终态 —— 那正是本包要治的「验后即弃」盲区）。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
)

// findTaskRow 从详情响应 tasks[] 取指定 task 行。
func findTaskRow(t *testing.T, data map[string]any, taskID string) map[string]any {
	t.Helper()
	rows, _ := data["tasks"].([]any)
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if m["task_id"] == taskID {
			return m
		}
	}
	t.Fatalf("详情 tasks[] 缺 task_id=%s", taskID)
	return nil
}

// TestBANodeRecordFieldsPersistN069 用例①＋③＋消费面：
// 推进到 return_receipt（disburse 带签领三字段）⇒ 详情任务行带 record_fields（T1-c）
// ⇒ 附件经暂存上传 ⇒ 带 fields＋attachment_ids 同意 ⇒ 终态 ＋ ext 落库 ＋ 附件已绑
// ＋ 越权键不落。
func TestBANodeRecordFieldsPersistN069(t *testing.T) {
	e, db, auth := newSubmitM4AppObj(t, true, nil, newMemObjectStore(), nil)
	cookie := auth.Establish("ou_app")
	ctx := context.Background()

	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("BA 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)

	// 推进：approve_petty_cash（空 fields 放行）→ disburse（签领三字段 ⇒ T1-a 落库面）。
	rrTask := driveUntilNode(t, e, db, auth, bizNo, "return_receipt", map[string]map[string]any{
		"disburse": {
			"petty_cash_receiver":       "张三",
			"petty_cash_received_cents": 50000,
			"petty_cash_received_at":    "2026-10-06",
		},
	})
	if rrTask == nil {
		t.Fatal("return_receipt 未按顺序释放")
	}

	// disburse 落库断言（签领三键进 ext ＝ 节点留存；台账 ops 面不动，R-36 分界）。
	ext0 := extOf(t, db, bizNo)
	if ext0["petty_cash_receiver"] != "张三" || ext0["petty_cash_received_cents"] == nil {
		t.Fatalf("disburse 签领字段未落 ext: %v", ext0)
	}

	// ★ T1-c 消费面：详情任务行必须带 record_fields（否则键＝死配置、前端无从渲染）。
	recD, envD := doRequest(e, http.MethodGet, "/api/approval/"+bizNo, cookie, "")
	if recD.Code != http.StatusOK {
		t.Fatalf("详情读取应 200, 实为 %d", recD.Code)
	}
	dd, _ := envD.Data.(map[string]any)
	row := findTaskRow(t, dd, rrTask.TaskID)
	rfRaw, ok := row["record_fields"].([]any)
	if !ok || len(rfRaw) != 2 {
		t.Fatalf("任务行缺 record_fields（两键），实为 %v", row["record_fields"])
	}
	rfByName := map[string]map[string]any{}
	for _, it := range rfRaw {
		m, _ := it.(map[string]any)
		rfByName[m["name"].(string)] = m
	}
	if rfByName["payment_receipt_no"]["label"] != "付款凭据号" || rfByName["payment_receipt_no"]["required"] != true {
		t.Errorf("payment_receipt_no 元数据须自 spec 读出（label/required）: %v", rfByName["payment_receipt_no"])
	}
	if rfByName["payment_receipt_file"]["type"] != "attachment" {
		t.Errorf("payment_receipt_file.type = %v, 期望 attachment", rfByName["payment_receipt_file"]["type"])
	}

	// 附件经**既有**暂存通道上传（owner＝申请人本人 ou_app；不新增端点）。
	uc, uenv := uploadFile(t, e, cookie, "receipt.pdf", []byte("%PDF-1.4 fake"))
	if uc != http.StatusOK {
		t.Fatalf("上传应 200, 实为 %d（%s）", uc, uenv.Message)
	}
	ud, _ := uenv.Data.(map[string]any)
	fileID, _ := ud["file_id"].(string)
	if fileID == "" {
		t.Fatalf("上传未返回 file_id: %v", ud)
	}

	// 同意：两键 fields（必填由 checkReceiptPerPurchase 承载）＋ attachment_ids ＋ 越权键。
	approveBody := fmt.Sprintf(
		`{"task_id":%q,"fields":{"payment_receipt_no":"PJ-001","payment_receipt_file":%q,"smuggled_key":"x"},"attachment_ids":[%q]}`,
		rrTask.TaskID, fileID, fileID)
	recA, envA := doRequest(e, http.MethodPost, "/api/approval/"+bizNo+"/approve", cookie, approveBody)
	if recA.Code != http.StatusOK {
		t.Fatalf("凭据齐全应放行并终态: %d（%s）", recA.Code, envA.Message)
	}

	// 落库断言（①真断言）。
	ext := extOf(t, db, bizNo)
	if ext["payment_receipt_no"] != "PJ-001" {
		t.Errorf("payment_receipt_no 未落 ext（验后即弃？）: %v", ext["payment_receipt_no"])
	}
	if ext["payment_receipt_file"] != fileID {
		t.Errorf("payment_receipt_file 未落 ext: %v", ext["payment_receipt_file"])
	}
	// ③ 越权键不落（只写 record_fields 声明键）。
	if _, has := ext["smuggled_key"]; has {
		t.Errorf("越权键 smuggled_key 不得落库: %v", ext)
	}
	// 附件已绑：t_attachment 行存在且指向本单。
	var bindBiz string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(biz_no,'') FROM t_attachment WHERE file_id=?`, fileID).Scan(&bindBiz); err != nil {
		t.Fatal(err)
	}
	if bindBiz != bizNo {
		t.Errorf("附件未绑定到本单: biz_no=%q", bindBiz)
	}
	// 终态。
	inst, err := db.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Status != "APPROVED" {
		t.Errorf("应终态 APPROVED, 实为 %s", inst.Status)
	}
}

// TestBANodeAttachmentRollbackN069 用例④：附件不可绑定 ⇒ 整体回滚
// （任务仍 PENDING、ext 不含凭据键、无半程副作用）＋ 400 点名 file_id。
func TestBANodeAttachmentRollbackN069(t *testing.T) {
	e, db, auth := newSubmitM4AppObj(t, true, nil, newMemObjectStore(), nil)
	cookie := auth.Establish("ou_app")

	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("BA 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)

	rrTask := driveUntilNode(t, e, db, auth, bizNo, "return_receipt", nil)
	if rrTask == nil {
		t.Fatal("return_receipt 未按顺序释放")
	}
	approveBody := fmt.Sprintf(
		`{"task_id":%q,"fields":{"payment_receipt_no":"PJ-002","payment_receipt_file":"f-x"},"attachment_ids":["bogus-file-xx"]}`,
		rrTask.TaskID)
	rec, envA := doRequest(e, http.MethodPost, "/api/approval/"+bizNo+"/approve", cookie, approveBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不可绑定附件应 400, 实为 %d（%s）", rec.Code, envA.Message)
	}
	if !strings.Contains(envA.Message, "bogus-file-xx") {
		t.Errorf("错误须点名 file_id, got: %s", envA.Message)
	}
	// 任务仍 PENDING（act 事务整体回滚 —— 不留半程）。
	tk := firstReleasedTask(t, db, bizNo, "return_receipt")
	if tk == nil || tk.Status != flow.TaskPending {
		t.Errorf("回滚后任务应仍 PENDING（已释放）: %+v", tk)
	}
	// ext 未写入凭据键（同事务回滚）。
	ext := extOf(t, db, bizNo)
	if _, has := ext["payment_receipt_no"]; has {
		t.Errorf("回滚后 ext 不得含凭据键: %v", ext)
	}
}
