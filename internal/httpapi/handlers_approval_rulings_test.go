package httpapi

// 裁定对齐验收：N-013（is_fixed_asset 分支可达）· N-014（会签标注 + ≥3 告警）。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// TestPreviewIsFixedAssetTriggersTier3Plus N-013：forms/PR 已补 is_fixed_asset，
// 金额未过 20 万但勾选固定资产 ⇒ 同样触发 tier3_plus 插入。
func TestPreviewIsFixedAssetTriggersTier3Plus(t *testing.T) {
	e, _, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")

	// ① 未勾选、金额 6 万（≤20 万）⇒ 不触发
	code, env := postPreview(t, e, cookie,
		`{"doc_type":"PR","amount_cents":600000,"usage_category_l1":"P04","department":"生产部"}`)
	if code != http.StatusOK {
		t.Fatalf("preview 失败 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	if hasProcureConfirm(d) {
		t.Error("未勾选固定资产且金额未过 20 万，不应触发 tier3_plus")
	}

	// ② 勾选 is_fixed_asset ⇒ 即使金额仅 6 万也触发
	code, env = postPreview(t, e, cookie,
		`{"doc_type":"PR","amount_cents":600000,"usage_category_l1":"P04","department":"生产部","is_fixed_asset":true}`)
	if code != http.StatusOK {
		t.Fatalf("preview 失败 %d：%s", code, env.Message)
	}
	d, _ = env.Data.(map[string]any)
	if !hasProcureConfirm(d) {
		t.Error("is_fixed_asset=true 应触发 tier3_plus（procure_method_confirm 插入）")
	}
}

func hasProcureConfirm(data map[string]any) bool {
	nodes, _ := data["nodes"].([]any)
	for _, n := range nodes {
		nm, _ := n.(map[string]any)
		if nm["source_node_id"] == "procure_method_confirm" {
			return true
		}
	}
	return false
}

// TestPreviewCoSignAnnotationAndWarning N-014：≥2 标注「N 人会签」；≥3 告警（不阻断）。
func TestPreviewCoSignAnnotationAndWarning(t *testing.T) {
	e, db, auth := newSubmitM4App(t, true)
	ctx := context.Background()
	// 综合运营部 ⇒ 主管领导解析为「项目总经理」（is_also_supervisor_for）；配 2 人 → 会签
	for i, id := range []string{"ou_g1", "ou_g2"} {
		if err := db.UpsertUserRole(ctx, store.UserRole{
			OpenID: id, Name: string(rune('A'+i)) + "总", Role: "项目总经理",
			Department: "综合运营部", Active: true, UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	cookie := auth.Establish("ou_app")

	// ① 2 人 ⇒ co_sign_count=2、branch_note 标注会签、无告警（2 < 3）
	code, env := postPreview(t, e, cookie,
		`{"doc_type":"PR","amount_cents":100000,"usage_category_l1":"P01","department":"综合运营部"}`)
	if code != http.StatusOK {
		t.Fatalf("preview 失败 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	node := findNode(d, "supervisor_approval")
	if node == nil {
		t.Fatal("缺 supervisor_approval 节点")
	}
	if cs, _ := node["co_sign_count"].(float64); int(cs) != 2 {
		t.Errorf("co_sign_count = %v，应为 2", node["co_sign_count"])
	}
	note, _ := node["branch_note"].(string)
	if !strings.Contains(note, "2 人会签") {
		t.Errorf("branch_note 未标注会签：%q", note)
	}
	if w, _ := d["warnings"].([]any); len(w) != 0 {
		t.Errorf("2 人不应告警：%v", w)
	}

	// ② 第 3 人 ⇒ 告警（≥ warn_threshold=3，不阻断）
	if err := db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_g3", Name: "C总", Role: "项目总经理",
		Department: "综合运营部", Active: true, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	code, env = postPreview(t, e, cookie,
		`{"doc_type":"PR","amount_cents":100000,"usage_category_l1":"P01","department":"综合运营部"}`)
	if code != http.StatusOK {
		t.Fatalf("preview 失败 %d：%s", code, env.Message)
	}
	d, _ = env.Data.(map[string]any)
	w, _ := d["warnings"].([]any)
	if len(w) == 0 {
		t.Fatal("3 人会签必须告警（N-014：≥warn_threshold 不阻断但须提示）")
	}
}

func findNode(data map[string]any, sourceID string) map[string]any {
	nodes, _ := data["nodes"].([]any)
	for _, n := range nodes {
		nm, _ := n.(map[string]any)
		if nm["source_node_id"] == sourceID {
			return nm
		}
	}
	return nil
}
