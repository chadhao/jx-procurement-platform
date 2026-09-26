package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 本文件覆盖 Q14 定稿（2026-09-26）后的两处台账读侧口径与字段定义白名单：
//
//	① L07 到货验收台账 —— GR 与 QC 各写一行 + 查询侧关联（Q14-B 第 1 项）
//	② L11 订单执行台账 —— 派生视图、不落行（Q14-B 第 2 项）
//	③ 台账字段定义（t_ledger_field_def）—— 写接口的 fields 键名白名单（Q14-B 第 4 项）

// seedArchiveDoc 写入台账行并**指定 source_doc_type**。
// 既有的 seedArchiveExt 把 source_doc_type 写死为 "CT"，无法表达 QC / GR 等单据类型，
// 而 L07 的跨单据关联正是按单据类型区分方向的。
func seedArchiveDoc(t *testing.T, db *store.DB, ledgerType, bizNo, docType, dept, applicant, bizDate string, amount int64, ext string) {
	t.Helper()
	now := time.Now().UTC()
	amt := amount
	if err := db.UpsertArchive(context.Background(), db, &store.LedgerArchive{
		LedgerType: ledgerType, BizNo: bizNo, InstanceCode: "I-" + bizNo, SourceDocType: docType,
		Department: dept, ApplicantOpenID: applicant, AmountCents: &amt,
		BizDate: bizDate, ExtJSON: ext, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入台账行失败: %v", err)
	}
}

// TestLedgerL07LinkedInspection L07：QC 单列一行，其检验结论关联到对应 GR 行。
func TestLedgerL07LinkedInspection(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})

	// GR 行（到货验收单）与其关联的 QC 行（来料检验报告，related_biz_no 指向 GR）。
	seedArchiveDoc(t, db, "L07", "GR-2609-0001", "GR", "生产部", "ou_a", "2026-09-13", 500000,
		`{"related_biz_no":"CT-2609-0001"}`)
	seedArchiveDoc(t, db, "L07", "QC-2609-0001", "QC", "生产部", "ou_a", "2026-09-14", 0,
		`{"related_biz_no":"GR-2609-0001","inspection_result":"合格","inspection_item":"粒度、水分"}`)
	// 另一笔脏 JSON —— 不得使整页查询失败。
	seedArchiveDoc(t, db, "L07", "QC-2609-0002", "QC", "销售部", "ou_b", "2026-09-14", 0, `not json`)

	rec, env := doRequest(e, http.MethodGet, "/api/ledger/L07", auth.Establish("ou_gm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("L07 列表: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	items := mustData(t, env)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("L07 行数 = %d, 期望 3（GR 1 + QC 2，QC 自身也是独立一行）", len(items))
	}

	var grRow, qcRow map[string]any
	for _, it := range items {
		m := it.(map[string]any)
		switch m["biz_no"] {
		case "GR-2609-0001":
			grRow = m
		case "QC-2609-0001":
			qcRow = m
		}
	}
	if grRow == nil || qcRow == nil {
		t.Fatalf("未取到 GR / QC 行: %v", items)
	}
	// ★ GR 行带上关联 QC 的检验结论。
	insp, ok := grRow["inspection"].(map[string]any)
	if !ok {
		t.Fatalf("GR 行未带上关联检验结论: %v", grRow)
	}
	if insp["biz_no"] != "QC-2609-0001" || insp["result"] != "合格" {
		t.Errorf("检验结论块 = %v, 期望 biz_no=QC-2609-0001 result=合格", insp)
	}
	if insp["item"] != "粒度、水分" {
		t.Errorf("检验项目 = %v", insp["item"])
	}
	// ★ QC 行反向给出它关联的 GR。
	qcInsp, ok := qcRow["inspection"].(map[string]any)
	if !ok || qcInsp["linked_biz_no"] != "GR-2609-0001" {
		t.Errorf("QC 行未给出关联 GR: %v", qcRow["inspection"])
	}
	// 脏 JSON 行的关联块不存在，但查询本身不得失败（已由 HTTP 200 保证）。
	if m := items[0].(map[string]any); m["biz_no"] == "QC-2609-0002" && m["inspection"] != nil {
		t.Errorf("脏 JSON 行不应有检验结论块: %v", m["inspection"])
	}
}

// TestLedgerL07InspectionRespectsRowScope 关联块同样受行级过滤：不得把别部门的检验结论挂过来。
func TestLedgerL07InspectionRespectsRowScope(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_lead", Role: roleDeptLead, Department: "生产部", Active: true})

	seedArchiveDoc(t, db, "L07", "GR-2609-0100", "GR", "生产部", "ou_a", "2026-09-13", 100, `{}`)
	// 检验报告落在**销售部**（他部门）→ 生产部主管不应看到它。
	seedArchiveDoc(t, db, "L07", "QC-2609-0100", "QC", "销售部", "ou_b", "2026-09-14", 0,
		`{"related_biz_no":"GR-2609-0100","inspection_result":"不合格"}`)

	rec, env := doRequest(e, http.MethodGet, "/api/ledger/L07", auth.Establish("ou_lead"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("L07 列表: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	items := mustData(t, env)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("生产部主管应只见 1 条（本部门 GR），实际 %d", len(items))
	}
	if _, ok := items[0].(map[string]any)["inspection"]; ok {
		t.Errorf("他部门的检验结论被挂到了本部门行上（行级过滤失效）")
	}
}

// TestLedgerL11Derived 由 L04（CT）+ L07（GR）派生的订单执行行：到货状态与延期天数。
func TestLedgerL11Derived(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})

	// 合同：交期 2026-09-10；另一份合同尚未到货。
	seedArchiveDoc(t, db, "L04", "CT-2609-0001", "CT", "生产部", "ou_a", "2026-09-01", 500000,
		`{"delivery_date":"2026-09-10"}`)
	seedArchiveDoc(t, db, "L04", "CT-2609-0002", "CT", "生产部", "ou_a", "2026-09-01", 300000,
		`{"delivery_date":"2026-09-20"}`)
	// 仅第一份有到货验收单，实际到货 2026-09-13 → 延期 3 天。
	seedArchiveDoc(t, db, "L07", "GR-2609-0001", "GR", "生产部", "ou_a", "2026-09-13", 500000,
		`{"related_biz_no":"CT-2609-0001"}`)

	rec, env := doRequest(e, http.MethodGet, "/api/ledger/L11", auth.Establish("ou_gm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("L11 派生列表: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if data["derived"] != true {
		t.Errorf("未标记为派生视图: %v", data["derived"])
	}
	items := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("派生行数 = %d, 期望 2（以合同台账为骨架）", len(items))
	}
	byNo := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byNo[m["biz_no"].(string)] = m
	}
	// 已到货：有 gr_biz_no、实际到货、延期天数。
	got1 := byNo["CT-2609-0001"]
	if got1 == nil {
		t.Fatalf("缺少 CT-2609-0001 派生行: %v", byNo)
	}
	if got1["gr_biz_no"] != "GR-2609-0001" || got1["actual_arrival"] != "2026-09-13" {
		t.Errorf("到货字段 = %v / %v", got1["gr_biz_no"], got1["actual_arrival"])
	}
	if got1["delay_days"] != float64(3) { // JSON 解码后为 float64
		t.Errorf("延期天数 = %v (%T), 期望 3", got1["delay_days"], got1["delay_days"])
	}
	if got1["order_state"] != "已到货" {
		t.Errorf("订单状态 = %v, 期望 已到货", got1["order_state"])
	}
	// 未到货：无 gr_biz_no、无延期天数（不得臆造 0）。
	got2 := byNo["CT-2609-0002"]
	if got2 == nil {
		t.Fatalf("缺少 CT-2609-0002 派生行")
	}
	if _, ok := got2["gr_biz_no"]; ok {
		t.Errorf("未到货行不应有 gr_biz_no: %v", got2)
	}
	if _, ok := got2["delay_days"]; ok {
		t.Errorf("未到货行不应出现 delay_days（不得臆造 0）: %v", got2["delay_days"])
	}
	if got2["order_state"] != "在途" {
		t.Errorf("订单状态 = %v, 期望 在途", got2["order_state"])
	}
}

// TestLedgerL11ReadOnly 派生台账无写入口、也不支持按 id 读取。
func TestLedgerL11ReadOnly(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	// 综合运营主管在台账上是 ALL + 非空 writable_fields，但 L11 属只读派生 → 必须拒绝。
	rec, env := doRequest(e, http.MethodPatch, "/api/ledger/L11/1", cookie, `{"fields":{"实际到货":"2026-09-13"}}`)
	if rec.Code != http.StatusConflict || env.Code != codeReadOnly {
		t.Errorf("L11 写入应被拒: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	// 按 id 读取同样不成立（派生行无稳定主键）。
	rec, env = doRequest(e, http.MethodGet, "/api/ledger/L11/1", cookie, "")
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Errorf("L11 按 id 读取应 400: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
}

// TestLedgerFieldWhitelist 台账字段定义成为写接口的键名白名单（Q14-B 第 4 项）。
func TestLedgerFieldWhitelist(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})

	// 为 L01 登记两个字段定义（写侧白名单）。
	for _, k := range []string{"付款凭据号", "抽查状态"} {
		if err := db.UpsertLedgerFieldDef(ctx, store.LedgerFieldDef{
			LedgerType: "L01", FieldKey: k,
		}); err != nil {
			t.Fatalf("写字段定义失败: %v", err)
		}
	}
	now := time.Now().UTC()
	if err := db.UpsertArchive(ctx, db, &store.LedgerArchive{
		LedgerType: "L01", BizNo: "BA-2609-0001", InstanceCode: "I-1", SourceDocType: "BA",
		Department: "生产部", ApplicantOpenID: "ou_a", BizDate: "2026-09-10",
		ExtJSON: "{}", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写台账行失败: %v", err)
	}
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM t_ledger_archive WHERE biz_no='BA-2609-0001'`).Scan(&id); err != nil {
		t.Fatalf("取 id 失败: %v", err)
	}
	url := "/api/ledger/L01/" + strconv.FormatInt(id, 10)
	cookie := auth.Establish("ou_ops")

	// ① 已登记字段 → 可写。
	rec, env := doRequest(e, http.MethodPatch, url, cookie, `{"fields":{"付款凭据号":"PZ-2026-0001"}}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("已登记字段应可写: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}

	// ② 在 writable_fields 白名单内、但**未登记字段定义** → 40901。
	rec, env = doRequest(e, http.MethodPatch, url, cookie, `{"fields":{"经办状态":"已完成"}}`)
	if rec.Code != http.StatusConflict || env.Code != codeReadOnly {
		t.Errorf("未登记字段应被拒: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
}

// TestLedgerFieldWhitelistAbsentKeepsBackwardCompat 未登记任何字段定义时不做键名限制（向后兼容）。
func TestLedgerFieldWhitelistAbsentKeepsBackwardCompat(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	now := time.Now().UTC()
	if err := db.UpsertArchive(ctx, db, &store.LedgerArchive{
		LedgerType: "L01", BizNo: "BA-2609-0002", InstanceCode: "I-2", SourceDocType: "BA",
		Department: "生产部", ApplicantOpenID: "ou_a", BizDate: "2026-09-10",
		ExtJSON: "{}", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写台账行失败: %v", err)
	}
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM t_ledger_archive WHERE biz_no='BA-2609-0002'`).Scan(&id); err != nil {
		t.Fatalf("取 id 失败: %v", err)
	}
	// 无字段定义 → 仍按 writable_fields 放行（不因新增白名单把既有写入口打死）。
	rec, env := doRequest(e, http.MethodPatch, "/api/ledger/L01/"+strconv.FormatInt(id, 10),
		auth.Establish("ou_ops"), `{"fields":{"经办状态":"已完成"}}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Errorf("无字段定义时应向后兼容放行: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
}
