package httpapi

// N-040 第 3 项 · 契约测试（根因修补）—— 以**前端真实载荷形状**驱动 handleApprovalSubmit，
// 不再只用 Go 侧手搓的理想 body（上一轮双向探针的覆盖盲区）：
//   ① 真实形状＝Submit.vue#doSubmit 产出：扁平 fields ＋ fields.detail 数组 ＋ **无顶层 amount_cents**
//      ⇒ 合规单必须 200（裁定②：缺失即正常）；
//   ② 重复段行级必填缺字段 ⇒ 400 可见失败（裁定③服务端半）；
//   ③ 带了 amount_cents 且与服务端汇总不一致 ⇒ **200 不拒单** ＋ 审计 warn
//      action=amount_vs_server_sum_mismatch（裁定③），定档仍用服务端值。

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// frontendShapePRBody 按 Submit.vue#doSubmit 的真实产出构造 PR 提交体：
// 顶层只有 doc_type/approval_code/usage 两键/fields/attachment_ids —— **无 amount_cents**。
// bodyExtra 用于注入偏离形状（如伪造 amount）。
const okRow = `{"material_name_spec":"滤布 1200mm","unit":"吨","quantity":1,` +
	`"estimated_unit_price_cents":100000}`

func frontendShapePRBody(detail, bodyExtra string) string {
	// ★ bodyExtra 注入**顶层**偏离（如伪造 amount_cents）—— 必须拼在 JSON 最外层
	//（fields 已闭合后），放错位置＝进了 fields ⇒ 顶层仍无 amount ⇒ mismatch 不触发
	// （这正是本测试第一版的错）。
	return `{"doc_type":"PR","approval_code":"code-pr",` +
		`"usage_category_l1":"P01","usage_category_l2":"主原料",` +
		`"fields":{` +
		`"usage_category_l1":"P01","usage_category_l2":"主原料","requirement_type":"常规",` +
		`"purpose":"补一批滤布","required_date":"2026-12-01","urgent_level":"常规",` +
		`"budget_subject":"生产预算","is_safety_or_special_equipment":false,"is_fixed_asset":false,` +
		`"detail":[` + detail + `]` +
		`}` +
		bodyExtra +
		`,"attachment_ids":[]}`
}

// TestPRSubmitFrontendShapeContract ①：真实形状（无顶层 amount_cents）⇒ 200。
func TestPRSubmitFrontendShapeContract(t *testing.T) {
	e, _, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")

	code, env := postSubmit(t, e, cookie, frontendShapePRBody(okRow, ""), "")
	if code != http.StatusOK {
		t.Fatalf("前端真实形状（无 amount_cents）应 200 —— PR 提交从 UI 不可达即 N-040 未修复：code=%d msg=%s",
			code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	if biz, _ := d["biz_no"].(string); !strings.HasPrefix(biz, "PR-") {
		t.Errorf("biz_no = %v", d["biz_no"])
	}
}

// TestPRSubmitRowLevelRequiredContract ②：重复段行级必填缺字段 ⇒ 400（可见失败）。
func TestPRSubmitRowLevelRequiredContract(t *testing.T) {
	e, _, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")

	// 行内删掉必填 material_name_spec
	body := strings.Replace(frontendShapePRBody(okRow, ""),
		`"detail":[{"material_name_spec":"滤布 1200mm","unit":"吨"`,
		`"detail":[{"unit":"吨"`, 1)
	code, env := postSubmit(t, e, cookie, body, "")
	if code != http.StatusBadRequest {
		t.Fatalf("行内缺必填应 400，实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "第 1 行") {
		t.Errorf("错误应点名行号（可见失败）: %s", env.Message)
	}
}

// TestPRSubmitAmountMismatchWarnsNotRejects ③：带 amount 且不一致 ⇒ 200 ＋ 审计 warn。
func TestPRSubmitAmountMismatchWarnsNotRejects(t *testing.T) {
	e, db, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")

	// 汇总＝100000 分；客户端低报 9999 ⇒ mismatch（不拒单）
	code, env := postSubmit(t, e, cookie, frontendShapePRBody(okRow, `,"amount_cents":9999`), "")
	if code != http.StatusOK {
		t.Fatalf("不一致不许拒单（裁定③）：code=%d msg=%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	biz, _ := d["biz_no"].(string)
	if !strings.HasPrefix(biz, "PR-") {
		t.Fatalf("biz_no = %v", d["biz_no"])
	}
	// N-041：审计 warn 落痕 —— TargetID ＝ **返回的真实 biz_no**（不再是 PR(unsaved) 占位）
	var target, result, detail string
	if err := db.QueryRowContext(context.Background(),
		`SELECT target_id, result, COALESCE(detail_json,'') FROM t_audit_log
WHERE action='amount_vs_server_sum_mismatch' ORDER BY id DESC LIMIT 1`).
		Scan(&target, &result, &detail); err != nil {
		t.Fatal("amount 不一致必须留 amount_vs_server_sum_mismatch 审计 warn（可见、但不拒单）: " + err.Error())
	}
	if target != biz {
		t.Errorf("审计 TargetID = %q, 期望真实 biz_no %q（N-041：占位符无法按单追查）", target, biz)
	}
	if result != "warn" {
		t.Errorf("审计 result = %q, 期望 warn（可见但不拒单）", result)
	}
	// DetailJSON 三要素保留
	for _, k := range []string{"client_amount_cents", "server_sum_cents", "doc_type"} {
		if !strings.Contains(detail, k) {
			t.Errorf("DetailJSON 缺要素 %s: %s", k, detail)
		}
	}
}

// TestPRSubmitFailNoMismatchAudit N-041 ④：Submit 失败（本例＝行级必填先拦）
// ⇒ **不写** amount_vs_server_sum_mismatch 审计（无单据可查，写了也无从追）。
// 同时带 mismatch 载荷 —— 信号已产生但落审计点在成功路径之后，失败路径不得泄漏。
func TestPRSubmitFailNoMismatchAudit(t *testing.T) {
	e, db, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")

	badRow := `{"unit":"吨","quantity":1,"estimated_unit_price_cents":100000}`
	code, _ := postSubmit(t, e, cookie,
		frontendShapePRBody(badRow, `,"amount_cents":1`), "")
	if code != http.StatusBadRequest {
		t.Fatalf("行级必填应先拦 400，实为 %d", code)
	}
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_audit_log WHERE action='amount_vs_server_sum_mismatch'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("Submit 失败不得写 mismatch 审计（N-041 ④），实为 %d 条", n)
	}
}
