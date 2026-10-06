package httpapi

// N-062 族 J3 · SA 侧（MIMO-NEXT-BATCH-27）：后置补录入口端到端。
// 覆盖包验收 ①–⑤ ＋ 行级 403 ＋ invoice 判据双向 ＋ ext 残留键不被冲 ＋ 未注册 fail-closed。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/labstack/echo/v4"
)

// postBackfill 调 POST /api/approval/{biz_no}/backfill（单次请求，返回 (状态码, 响应)）。
func postBackfill(t *testing.T, e *echo.Echo, auth *access.Authenticator, biz, cookie string, fields map[string]any) (int, Envelope) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		t.Fatal(err)
	}
	rec, env := doRequest(e, http.MethodPost, "/api/approval/"+biz+"/backfill", cookie, string(raw))
	return rec.Code, env
}

// approvedSABiz 提交 SA 并推到 APPROVED（返回 biz_no；申请人＝ou_app）。
func approvedSABiz(t *testing.T, e *echo.Echo, db *store.DB, auth *access.Authenticator) string {
	t.Helper()
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, saBody(nil), "")
	if code != http.StatusOK {
		t.Fatalf("SA 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	biz, _ := d["biz_no"].(string)
	driveToTerminal(t, e, db, auth, biz, nil)
	inst, err := db.GetInstanceByBizNo(context.Background(), biz)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Status != "APPROVED" {
		t.Fatalf("SA 应推到 APPROVED, 实为 %s", inst.Status)
	}
	return biz
}

func backfillExt(t *testing.T, db *store.DB, biz string) map[string]any {
	t.Helper()
	inst, err := db.GetInstanceByBizNo(context.Background(), biz)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if inst.ExtJSON != "" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &m); err != nil {
			t.Fatalf("ext 解析失败: %v", err)
		}
	}
	return m
}

// TestSABackfillE2EB27 包验收 ①–⑤ ＋ 403/双向/残留键。
func TestSABackfillE2EB27(t *testing.T) {
	e, db, auth := newSASubmitApp(t)
	cookie := auth.Establish("ou_app")
	other := auth.Establish("ou_other") // 非申请人
	// 行级对照用户（无角色亦可——门在申请人比对，先建会话即可；ResolveRole 对未知 open_id
	// 需要镜像行，seed 一条以免 401/映射失败）。
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_other", Role: "申请人", Department: "生产部", Active: true})

	biz := approvedSABiz(t, e, db, auth)

	// ④ PENDING 实例 ⇒ 可见拒绝（409）。
	codeP, envP := postSubmit(t, e, cookie, saBody(nil), "")
	if codeP != http.StatusOK {
		t.Fatalf("第二张 SA 应 200, 实为 %d", codeP)
	}
	dP, _ := envP.Data.(map[string]any)
	pendingBiz, _ := dP["biz_no"].(string)
	code4, env4 := postBackfill(t, e, auth, pendingBiz, cookie, map[string]any{"invoice_info": "x"})
	if code4 != http.StatusConflict {
		t.Errorf("PENDING 实例应 409, 实为 %d（%s）", code4, env4.Message)
	}

	// 行级：非申请人 ⇒ 403。
	codeR, envR := postBackfill(t, e, auth, biz, other, map[string]any{"invoice_info": "x"})
	if codeR != http.StatusForbidden {
		t.Errorf("非申请人应 403, 实为 %d（%s）", codeR, envR.Message)
	}

	// ① 超支 ⇒ 拦（400 点名判据）；且**零写入**（invoice_info 不落）。
	code1, env1 := postBackfill(t, e, auth, biz, cookie, map[string]any{
		"actual_cents": 123400, // 批准额 50000（saBody 默认）
		"invoice_info": "票据 att://inv-1",
	})
	if code1 != http.StatusBadRequest {
		t.Fatalf("超支应 400, 实为 %d（%s）", code1, env1.Message)
	}
	if !strings.Contains(env1.Message, "actual_not_exceed") {
		t.Errorf("须点名 SA#actual_not_exceed, got: %s", env1.Message)
	}
	if _, written := backfillExt(t, db, biz)["invoice_info"]; written {
		t.Errorf("判据失败不得留下半程写入（键级合并应在全过后才落库）")
	}

	// invoice 反向：actual 未填（未填≠0 ⇒ 跳过 actual 判据）且 invoice 空 ⇒ 拦 invoice_must_link。
	codeI, envI := postBackfill(t, e, auth, biz, cookie, map[string]any{"handler": "张三"})
	if codeI != http.StatusBadRequest {
		t.Fatalf("invoice 缺失应 400, 实为 %d（%s）", codeI, envI.Message)
	}
	if !strings.Contains(envI.Message, "invoice_must_link") {
		t.Errorf("须点名 SA#invoice_must_link, got: %s", envI.Message)
	}

	// 预置 ext 残留键（applicant_department 同族），验证 ③ 键级合并不冲掉它。
	if err := seedBackfillResidual(t, db, biz); err != nil {
		t.Fatal(err)
	}

	// ② 合规补录 ⇒ 200，written 含两键，checks 两条 passed。
	code2, env2 := postBackfill(t, e, auth, biz, cookie, map[string]any{
		"actual_cents": 30000,
		"invoice_info": "票据 att://inv-2",
	})
	if code2 != http.StatusOK {
		t.Fatalf("合规补录应 200, 实为 %d（%s）", code2, env2.Message)
	}
	d2, _ := env2.Data.(map[string]any)
	if d2["section_id"] != "settlement_backfill" {
		t.Errorf("section_id = %v", d2["section_id"])
	}
	written, _ := d2["written"].([]any)
	wset := map[string]bool{}
	for _, w := range written {
		wset[w.(string)] = true
	}
	for _, want := range []string{"actual_cents", "invoice_info"} {
		if !wset[want] {
			t.Errorf("written 缺 %s: %v", want, written)
		}
	}
	checks, _ := d2["checks"].([]any)
	if len(checks) != 2 {
		t.Fatalf("checks 应含两条已执行判据, 实为 %v", checks)
	}
	ids := map[string]bool{}
	for _, c := range checks {
		m, _ := c.(map[string]any)
		ids[m["id"].(string)] = true
		if m["passed"] != true {
			t.Errorf("checks[%v] 应 passed=true", m)
		}
	}
	if !ids["actual_not_exceed"] || !ids["invoice_must_link"] {
		t.Errorf("checks 缺判据: %v", ids)
	}
	ext := backfillExt(t, db, biz)
	if ext["actual_cents"] == nil || ext["invoice_info"] == nil {
		t.Errorf("补录值未落 ext: %v", ext)
	}
	if ext["applicant_department"] == nil {
		t.Errorf("既有键 applicant_department 被冲掉（键级合并失败）: %v", ext)
	}

	// ③ 白名单外键 ⇒ 可见拒绝（header 段字段 ＋ 同段 computed 字段各一）。
	for _, bad := range []string{"amount_cents", "actual_vs_approved_diff_cents"} {
		code3, env3 := postBackfill(t, e, auth, biz, cookie, map[string]any{bad: 1})
		if code3 != http.StatusBadRequest {
			t.Errorf("白名单外键 %s 应 400, 实为 %d（%s）", bad, code3, env3.Message)
		}
		if !strings.Contains(env3.Message, "白名单") {
			t.Errorf("拒绝文案须点名白名单, got: %s", env3.Message)
		}
	}

	// ⑤ 写后可读：GET /api/approval/{biz_no} 的 fields 出口读回补录值。
	recG, envG := doRequest(e, http.MethodGet, "/api/approval/"+biz, cookie, "")
	if recG.Code != http.StatusOK {
		t.Fatalf("详情读回应 200, 实为 %d", recG.Code)
	}
	dG, _ := envG.Data.(map[string]any)
	fG, _ := dG["fields"].(map[string]any)
	if s, _ := fG["invoice_info"].(string); !strings.Contains(s, "inv-2") {
		t.Errorf("详情 fields 未读回补录值: %v", fG)
	}
}

// seedBackfillResidual 给已批实例的 ext 预置残留键（模拟 applicant_department 等历史残留）。
func seedBackfillResidual(t *testing.T, db *store.DB, biz string) error {
	t.Helper()
	inst, err := db.GetInstanceByBizNo(context.Background(), biz)
	if err != nil {
		return err
	}
	m := map[string]any{}
	if inst.ExtJSON != "" {
		_ = json.Unmarshal([]byte(inst.ExtJSON), &m)
	}
	m["applicant_department"] = "生产部"
	b, _ := json.Marshal(m)
	inst.ExtJSON = string(b)
	return db.UpsertInstance(context.Background(), inst)
}

// TestBackfillFailClosedUnregisteredB27 未注册 backfill hard 判据 ⇒ 可见失败。
func TestBackfillFailClosedUnregisteredB27(t *testing.T) {
	ghost := specload.FormDoc{
		Sections: []specload.SectionDoc{{ID: backfillSectionID}},
		Checks: []specload.CheckDoc{{
			ID: "ghost_backfill_check", Severity: "hard",
			When: "backfill(" + backfillSectionID + ")", CarriedByKind: "code",
		}},
	}
	d := Deps{Spec: &specload.Bundle{Forms: map[string]specload.FormDoc{"ZZ": ghost}}}
	inst := &store.Instance{DocType: "ZZ"}
	_, err := d.evaluateBackfillChecks(inst, map[string]any{})
	if err == nil {
		t.Fatal("未注册 backfill hard 判据必须可见失败（静默跳过 ⇒ 红）")
	}
	if !strings.Contains(err.Error(), "未注册求值器") || !strings.Contains(err.Error(), "ghost_backfill_check") {
		t.Errorf("错误须点名判据与根因, got: %v", err)
	}
}
