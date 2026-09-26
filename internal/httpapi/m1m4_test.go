package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// 本文件覆盖 M1 集团报销跟踪表（FR-M1-02）与 M4 变更链回溯（FR-M4-07）。

// seedArchiveExt 写入带自定义 ext_json 的台账存档行（供变更链用例）。
func seedArchiveExt(t *testing.T, db *store.DB, lt, bizNo, applicant, dept string, amount int64, extJSON string) {
	t.Helper()
	now := time.Now().UTC()
	amt := amount
	if err := db.UpsertArchive(context.Background(), db, &store.LedgerArchive{
		LedgerType: lt, BizNo: bizNo, InstanceCode: "I-" + bizNo, SourceDocType: "CT",
		Department: dept, ApplicantOpenID: applicant, AmountCents: &amt,
		BizDate: "2026-09-10", ExtJSON: extJSON, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入台账存档失败: %v", err)
	}
}

// TestReimbursementCreateListPatchRoles 报销跟踪：写＝综合运营主管；读＝主管领导/项目总经理；申请人被拒。
func TestReimbursementCreateListPatchRoles(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true},
		store.UserRole{OpenID: "ou_lead", Role: roleDeptLead, Department: "生产部", Active: true},
		store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true},
		store.UserRole{OpenID: "ou_app", Role: "申请人", Active: true},
	)

	opsCookie := auth.Establish("ou_ops")
	rec, env := doRequest(e, http.MethodPost, "/api/reimbursement", opsCookie,
		`{"src_biz_no":"SA-2609-0001","applicant_open_id":"ou_app","department":"生产部","actual_cents":123456,"invoice_count":3,"review_state":"初审通过","handover_date":"2026-09-20"}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("综合运营主管登记报销跟踪: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	id := int64(mustData(t, env)["id"].(float64))
	if id <= 0 {
		t.Fatalf("返回 id 非法: %d", id)
	}

	// 主管领导（读）可列。
	rec, env = doRequest(e, http.MethodGet, "/api/reimbursement", auth.Establish("ou_lead"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("主管领导读报销跟踪: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	if total := mustData(t, env)["total"].(float64); total != 1 {
		t.Errorf("报销跟踪 total = %v, 期望 1", total)
	}

	// 项目总经理（读）可列。
	rec, env = doRequest(e, http.MethodGet, "/api/reimbursement", auth.Establish("ou_gm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Errorf("项目总经理读报销跟踪: http=%d code=%d, 期望 200/0", rec.Code, env.Code)
	}

	// 申请人无权限。
	rec, env = doRequest(e, http.MethodGet, "/api/reimbursement", auth.Establish("ou_app"), "")
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Errorf("申请人读报销跟踪: http=%d code=%d, 期望 403/40300", rec.Code, env.Code)
	}
	// 申请人不可写。
	rec, env = doRequest(e, http.MethodPost, "/api/reimbursement", auth.Establish("ou_app"),
		`{"src_biz_no":"SA-2609-9999","actual_cents":100}`)
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Errorf("申请人登记报销跟踪: http=%d code=%d, 期望 403/40300", rec.Code, env.Code)
	}

	// 更新集团侧付款字段（人工登记）。
	rec, env = doRequest(e, http.MethodPatch, "/api/reimbursement/"+itoaTest(id), opsCookie,
		`{"paid_date":"2026-09-28","paid_cents":123000,"review_state":"集团已付款"}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("更新集团付款字段: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}

	rec, env = doRequest(e, http.MethodGet, "/api/reimbursement?src_biz_no=SA-2609-0001", opsCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("回查报销跟踪: http=%d", rec.Code)
	}
	items := mustData(t, env)["items"].([]any)
	row := items[0].(map[string]any)
	if row["paid_cents"].(float64) != 123000 {
		t.Errorf("paid_cents = %v, 期望 123000", row["paid_cents"])
	}
	if row["source"] != "人工登记" {
		t.Errorf("source = %v, 期望「人工登记」（FR-M6-07 同源：不回填）", row["source"])
	}
}

// TestReimbursementValidation 关键校验：单号必填、金额为正、状态枚举、日期格式。
func TestReimbursementValidation(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	cases := []struct {
		name string
		body string
	}{
		{"缺单号", `{"actual_cents":100}`},
		{"金额非正", `{"src_biz_no":"SA-1","actual_cents":0}`},
		{"非法状态", `{"src_biz_no":"SA-1","actual_cents":100,"review_state":"不存在"}`},
		{"日期格式", `{"src_biz_no":"SA-1","actual_cents":100,"handover_date":"2026/09/20"}`},
	}
	for _, tc := range cases {
		rec, env := doRequest(e, http.MethodPost, "/api/reimbursement", cookie, tc.body)
		if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
			t.Errorf("%s: http=%d code=%d, 期望 400/40000", tc.name, rec.Code, env.Code)
		}
	}
}

// TestContractChangesBacktrace 按合同号回溯变更：次数、累计金额、档位（max 取档）。
//
// ★ Q14 定稿（2026-09-26）后匹配口径已收敛为**单一键名** `contract_no`
// （原「键名无关匹配」是 Q14 未定稿时的兼容做法，现已作废）。故本用例断言：
//
//	① 同一合同号写在 `contract_no` 上的行**必须命中**；
//	② 同一合同号写在**别的键**（如 `related_contract`）上的行**不得命中** ——
//	   键名自此是契约；否则「恰好等于合同号的无关值」也会被拉进来；
//	③ 不相关合同、脏 JSON 行不得混入，且不得使查询报错。
func TestContractChangesBacktrace(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})

	// 两笔指向 CT-2609-0001，但只有第一笔把合同号写在约定的 contract_no 键上。
	seedArchiveExt(t, db, "L09", "CH-2609-0001", "ou_a", "生产部", 100000,
		`{"contract_no":"CT-2609-0001","change_cents":100000,"original_cents":500000}`)
	// ★ 键名不合约定（related_contract）——按新契约**不得命中**。
	seedArchiveExt(t, db, "L09", "CH-2609-0002", "ou_a", "生产部", 200000,
		`{"related_contract":"CT-2609-0001","change_cents":200000,"original_cents":500000}`)
	// 另一笔按约定键名、但指向别的合同 —— 不得命中。
	seedArchiveExt(t, db, "L09", "CH-2609-0003", "ou_b", "销售部", 300000,
		`{"contract_no":"CT-2609-0002","change_cents":300000,"original_cents":900000}`)
	// 脏 JSON —— 不得使查询报错。
	seedArchiveExt(t, db, "L09", "CH-2609-0004", "ou_b", "销售部", 1, `not json`)

	rec, env := doRequest(e, http.MethodGet, "/api/contract/CT-2609-0001/changes", auth.Establish("ou_gm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("变更链查询: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if got := data["count"].(float64); got != 1 {
		t.Errorf("变更次数 = %v, 期望 1（仅约定的 contract_no 键命中；键名不合约定/异合同/脏 JSON 均不得混入）", got)
	}
	if got := data["cumulative_change_cents"].(float64); got != 100000 {
		t.Errorf("累计变更金额 = %v, 期望 100000", got)
	}
	if got := data["cumulative_change_display"]; got != "1,000.00" {
		t.Errorf("累计展示 = %v, 期望 1,000.00", got)
	}
	items := data["items"].([]any)
	first := items[0].(map[string]any)
	if got := first["tier"]; got != "max 取档 → 5,000.00" {
		t.Errorf("档位 = %v, 期望「max 取档 → 5,000.00」（A8：max(变更差额, 原合同金额)）", got)
	}
}

// TestContractChangesSingleKeyContract 固化「单一键名」契约：键名写错即查不到（而非"猜任意键"）。
func TestContractChangesSingleKeyContract(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})

	// 值正确但键名写错（历史上"键名无关匹配"会放行）→ 现必须查不到。
	seedArchiveExt(t, db, "L09", "CH-2609-9001", "ou_a", "生产部", 100,
		`{"related_contract":"CT-9999-0001","change_cents":100}`)
	rec, env := doRequest(e, http.MethodGet, "/api/contract/CT-9999-0001/changes", auth.Establish("ou_gm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("查询应成功但无结果: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	if got := mustData(t, env)["count"].(float64); got != 0 {
		t.Errorf("键名不合约定却命中 %v 条，期望 0（键名是契约）", got)
	}

	// 改用约定键名 → 必须命中。
	seedArchiveExt(t, db, "L09", "CH-2609-9002", "ou_a", "生产部", 100,
		`{"contract_no":"CT-9999-0001","change_cents":100}`)
	rec, env = doRequest(e, http.MethodGet, "/api/contract/CT-9999-0001/changes", auth.Establish("ou_gm"), "")
	if got := mustData(t, env)["count"].(float64); got != 1 {
		t.Errorf("约定键名命中 %v 条，期望 1", got)
	}
}

// TestExtKeyRelatedBizNo 关联键 related_biz_no：按约定键取值，脏 JSON 不炸查询（供单号关联用）。
func TestExtKeyRelatedBizNo(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	amt := int64(100)
	must := func(bizNo, ext string) {
		t.Helper()
		if err := db.UpsertArchive(ctx, db, &store.LedgerArchive{
			LedgerType: "L07", BizNo: bizNo, InstanceCode: "I-" + bizNo, SourceDocType: "QC",
			Department: "生产部", ApplicantOpenID: "ou_a", AmountCents: &amt,
			BizDate: "2026-09-10", ExtJSON: ext, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	must("QC-2609-0001", `{"related_biz_no":"GR-2609-0001","inspection_result":"合格"}`)
	must("QC-2609-0002", `{"related_biz_no":"GR-2609-0002","inspection_result":"让步接收"}`)
	must("QC-2609-0003", `not json`)

	got, err := db.ListArchiveByExtKey(ctx, "L07", store.KeyRelatedBizNo, "GR-2609-0001", "", nil)
	if err != nil {
		t.Fatalf("按 related_biz_no 查询失败（脏 JSON 不得使查询报错）: %v", err)
	}
	if len(got) != 1 || got[0].BizNo != "QC-2609-0001" {
		t.Fatalf("命中 = %+v, 期望仅 QC-2609-0001", got)
	}
	if r := got[0].ExtString(store.KeyRelatedBizNo); r != "GR-2609-0001" {
		t.Errorf("ExtString(related_biz_no) = %q", r)
	}
	if r := got[0].ExtString("inspection_result"); r != "合格" {
		t.Errorf("ExtString(inspection_result) = %q, 期望 合格", r)
	}
	if r := got[0].ExtString("不存在的键"); r != "" {
		t.Errorf("未知键应返回空串，实际 %q", r)
	}
}

// TestContractChangesRowScope 行级过滤：仅见本部门变更（DEPT 令牌）。
func TestContractChangesRowScope(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_prod", Role: roleDeptLead, Department: "生产部", Active: true},
		store.UserRole{OpenID: "ou_sales", Role: roleDeptLead, Department: "销售部", Active: true},
	)
	seedArchiveExt(t, db, "L09", "CH-2609-0011", "ou_a", "生产部", 100000,
		`{"contract_no":"CT-2609-0010","change_cents":100000}`)
	seedArchiveExt(t, db, "L09", "CH-2609-0012", "ou_b", "销售部", 200000,
		`{"contract_no":"CT-2609-0010","change_cents":200000}`)

	rec, env := doRequest(e, http.MethodGet, "/api/contract/CT-2609-0010/changes", auth.Establish("ou_prod"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("生产部主管读变更链: http=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := mustData(t, env)["count"].(float64); got != 1 {
		t.Errorf("生产部主管可见变更数 = %v, 期望 1（只应见本部门）", got)
	}
}

// TestContractChangesDeniedRole 默认拒绝角色的越权访问必须被拦并留痕。
func TestContractChangesDeniedRole(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_fin", Role: "集团财务", Active: true})

	rec, env := doRequest(e, http.MethodGet, "/api/contract/CT-2609-0001/changes", auth.Establish("ou_fin"), "")
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Errorf("集团财务读变更链: http=%d code=%d, 期望 403/40300", rec.Code, env.Code)
	}
}

// itoaTest 将 int64 转十进制字符串（避免测试内多处 strconv 引入）。
func itoaTest(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestContractChangesHidesAmountFlavouredKeys ★ 禁金额角色不得从变更链读到金额（B31 / TC-49）。
//
// 背景：`amountDeny` 写的是**规范键名** `amount_cents`，而变更链接口返回的是
// `change_cents` / `original_cents` / `*_display`，且原样回出的 `archive` 块里还有一份。
// 若投影只按字面 deny 匹配，禁金额角色会**照常读到金额** —— 实打实的越权（且不报错）。
//
// 本用例同时覆盖三层：顶层同义键、嵌套 archive 内的同义键、以及"非金额键必须留下"。
func TestContractChangesHidesAmountFlavouredKeys(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	// 采购经办人：row_scope=ASSIGNED 且**禁金额**（amountDeny）。
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_h", Role: "采购经办人", Active: true},
		store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true},
	)
	// 变更单：既满足 ASSIGNED 的行可见性，又带齐各种金额键名。
	seedArchiveExt(t, db, "L09", "CH-2609-1001", "ou_a", "生产部", 100000,
		`{"contract_no":"CT-2609-1001","assigned_open_id":"ou_h",
		  "change_cents":100000,"change_display":"1,000.00",
		  "original_cents":500000,"original_display":"5,000.00"}`)

	// ① 采购经办人（禁金额）：金额类键名在**任意层级**都不得出现。
	rec, env := doRequest(e, http.MethodGet, "/api/contract/CT-2609-1001/changes", auth.Establish("ou_h"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("变更链查询: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if data["amount_hidden"] != true {
		t.Errorf("amount_hidden = %v, 期望 true（禁金额角色）", data["amount_hidden"])
	}
	if _, ok := data["cumulative_change_cents"]; ok {
		t.Errorf("禁金额角色仍收到 cumulative_change_cents")
	}
	items := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("变更条数 = %d, 期望 1（行级 ASSIGNED 应命中）", len(items))
	}
	item := items[0].(map[string]any)
	for _, k := range []string{"change_cents", "change_display", "original_cents", "original_display"} {
		if _, ok := item[k]; ok {
			t.Errorf("禁金额角色仍收到 %s（同义键未被裁剪）", k)
		}
	}
	// 嵌套 archive 块：金额类键同样必须消失，**非金额键必须留下**（allow 不递归套用）。
	arc, ok := item["archive"].(map[string]any)
	if !ok {
		t.Fatalf("archive 块缺失或类型异常: %T", item["archive"])
	}
	for _, k := range []string{"change_cents", "change_display", "original_cents", "original_display"} {
		if _, ok := arc[k]; ok {
			t.Errorf("archive 内仍含金额类键 %s", k)
		}
	}
	if arc["contract_no"] != "CT-2609-1001" {
		t.Errorf("archive 内非金额键被误删: %v", arc)
	}

	// ② 项目总经理（不禁金额）：金额类键必须**照常可见**（不得因收紧而丢合法数据）。
	_, envGM := doRequest(e, http.MethodGet, "/api/contract/CT-2609-1001/changes", auth.Establish("ou_gm"), "")
	dataGM := mustData(t, envGM)
	itemGM := dataGM["items"].([]any)[0].(map[string]any)
	if _, ok := itemGM["change_cents"]; !ok {
		t.Errorf("项目总经理应能看到 change_cents（收紧过度）")
	}
	if _, ok := dataGM["cumulative_change_cents"]; !ok {
		t.Errorf("项目总经理应能看到累计变更金额（收紧过度）")
	}
	// 顺带固化既有的 max 取档口径。
	if itemGM["tier"] != "max 取档 → 5,000.00" {
		t.Errorf("档位 = %v, 期望「max 取档 → 5,000.00」", itemGM["tier"])
	}
}
