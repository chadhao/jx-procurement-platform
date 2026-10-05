package httpapi

// N-062 J1：PC/SS 档位审批记录终态生产者（flow.WriteTierApprovalRecord）的端到端判据。
// 断言：实例 ext 字段 ＋ L09 台账行 approval_record 列双落、内容含节点/审批人/同意。

import (
	"context"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

func tierRecordAssert(t *testing.T, db *store.DB, bizNo, fieldKey string) {
	t.Helper()
	ext := extOf(t, db, bizNo)
	rec, _ := ext[fieldKey].(string)
	if rec == "" {
		t.Fatalf("实例 ext.%s 缺失（终态生产者未落）: %v", fieldKey, ext)
	}
	if !strings.Contains(rec, "同意") {
		t.Errorf("%s = %q, 期望含 决议（同意）", fieldKey, rec)
	}
	// 含审批人姓名（任务 assignee 名必须出现在记录里）
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatal(err)
	}
	named := ""
	for _, tk := range tasks {
		if tk.AssigneeName != "" {
			named = tk.AssigneeName
			break
		}
	}
	if named == "" {
		t.Fatalf("任务无 assignee 姓名，夹具异常（biz=%s）", bizNo)
	}
	if !strings.Contains(rec, named) {
		t.Errorf("%s = %q, 期望含审批人 %q", fieldKey, rec, named)
	}
	// L09 台账行 approval_record 列同值（台账页「审批记录」列读它）
	l09 := l09Row(t, db, bizNo)
	if l09["approval_record"] != rec {
		t.Errorf("L09 approval_record = %v, 期望与实例字段同值 %q", l09["approval_record"], rec)
	}
}

func TestSSChainRecordWrittenAtTerminal(t *testing.T) {
	e, db, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, ssSubmitBody(), "")
	if code != 200 {
		t.Fatalf("SS 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)
	driveToTerminal(t, e, db, auth, bizNo, map[string]map[string]any{
		"tech_opinion": {"tech_opinion": "技术评估符合使用要求"},
		"pgm_final":    {"pgm_final_opinion": "同意独家采购"},
	})
	tierRecordAssert(t, db, bizNo, "tier_chain_record")
}

func TestPCTierApprovalRecordWrittenAtTerminal(t *testing.T) {
	e, db, auth := newSSPCApp(t)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, pcSubmitBody(""), "")
	if code != 200 {
		t.Fatalf("PC 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)
	driveToTerminal(t, e, db, auth, bizNo, map[string]map[string]any{
		"ledger_submit": {"resubmitted_to_group_at": "2026-10-04"},
	})
	tierRecordAssert(t, db, bizNo, "tier_approval_record")
}
