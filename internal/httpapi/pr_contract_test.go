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
func frontendShapePRBody(bodyExtra string) string {
	// ★ bodyExtra 注入**顶层**偏离（如伪造 amount_cents）—— 必须拼在 JSON 最外层
	//（fields 已闭合后），放错位置＝进了 fields ⇒ 顶层仍无 amount ⇒ mismatch 不触发
	// （这正是本测试第一版的错）。
	return `{"doc_type":"PR","approval_code":"code-pr",` +
		`"usage_category_l1":"P01","usage_category_l2":"主原料",` +
		`"fields":{` +
		`"usage_category_l1":"P01","usage_category_l2":"主原料","requirement_type":"常规",` +
		`"purpose":"补一批滤布","required_date":"2026-12-01","urgent_level":"常规",` +
		`"budget_subject":"生产预算","is_safety_or_special_equipment":false,"is_fixed_asset":false,` +
		`"detail":[{"material_name_spec":"滤布 1200mm","unit":"吨","quantity":1,` +
		`"estimated_unit_price_cents":100000}]` +
		`}` +
		bodyExtra +
		`,"attachment_ids":[]}`
}

// TestPRSubmitFrontendShapeContract ①：真实形状（无顶层 amount_cents）⇒ 200。
func TestPRSubmitFrontendShapeContract(t *testing.T) {
	e, _, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")

	code, env := postSubmit(t, e, cookie, frontendShapePRBody(""), "")
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
	body := strings.Replace(frontendShapePRBody(""),
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
	code, env := postSubmit(t, e, cookie, frontendShapePRBody(`,"amount_cents":9999`), "")
	if code != http.StatusOK {
		t.Fatalf("不一致不许拒单（裁定③）：code=%d msg=%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	biz, _ := d["biz_no"].(string)
	if !strings.HasPrefix(biz, "PR-") {
		t.Fatalf("biz_no = %v", d["biz_no"])
	}
	// 审计 warn 落痕（target_id 兼容 unsaved 占位与回填单号两种形态）
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_audit_log WHERE action='amount_vs_server_sum_mismatch'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("amount 不一致必须留 amount_vs_server_sum_mismatch 审计 warn（可见、但不拒单）")
	}
	var result string
	if err := db.QueryRowContext(context.Background(),
		`SELECT result FROM t_audit_log WHERE action='amount_vs_server_sum_mismatch' LIMIT 1`).
		Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "warn" {
		t.Errorf("审计 result = %q, 期望 warn（可见但不拒单）", result)
	}
}
