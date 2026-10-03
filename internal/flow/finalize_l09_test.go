package flow_test

// N-036 · PC/SS#ledger_l09_written：finalize 落 L09 后的**列自检**（else=告警）。
//   断言的列由提交期注入（httpapi#injectPCSSSystemFields）生产 ——
//   本测试模拟「注入已完成」的 fields（flow 直调不经过 handler）：
//   ① 列齐 ⇒ L09 行存在且**无** `ledger_l09_column_missing` 审计；
//   ② 列缺（生产者被绕过）⇒ 审计 warn 行出现（告警可见，不中断终态）。

import (
	"context"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// submitPCWithFields 提交 PC（链自定义 1 节点即终态），fields 已带注入值。
func submitPCWithFields(t *testing.T, svc *flow.Service, fields map[string]any) string {
	t.Helper()
	amt := int64(1000000)
	in := flow.SubmitInput{
		DocType: "PC", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		AmountCents: &amt, Department: "生产部",
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:    flowAt,
	}
	// fields 经 BizFields 进 ext（与 handler 注入后同路径）
	in.BizFields = fields
	bizNo, err := svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

func TestFinalizeL09ColumnsSelfCheck(t *testing.T) {
	db := newFlowDB(t)
	maps := &config.Maps{Ledger: map[string][]string{"PC": {"L09"}}}
	svc := flow.NewWithConfig(db, "app", maps, nil)
	ctx := context.Background()

	// ① 列齐（注入后形态）⇒ L09 行在、列在、无告警审计
	bizNo := submitPCWithFields(t, svc, map[string]any{
		"contract_no":             "CT-2609-0001",
		"exception_type":          "采购变更",
		"change_chain":            map[string]any{"count": 2, "cumulative_change_cents": 50000, "tier": "purchase_tier2"},
		"change_count_to_date":    2,
		"is_anomaly_listed":       true,
		"resubmitted_to_group_at": "2026-10-03",
		"applicable_tier":         "purchase_tier2",
	})
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_x").TaskID, "ou_x", "同意", nil); err != nil {
		t.Fatal(err)
	}
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type='L09' AND biz_no=?`, bizNo); n != 1 {
		t.Fatalf("L09 行数 = %d, 期望 1", n)
	}
	var extRaw string
	if err := db.QueryRowContext(ctx,
		`SELECT ext_json FROM t_ledger_archive WHERE ledger_type='L09' AND biz_no=?`, bizNo).Scan(&extRaw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"exception_type", "change_chain", "change_count_to_date",
		"is_anomaly_listed", "resubmitted_to_group_at", "applicable_tier"} {
		if !contains(extRaw, `"`+k+`"`) {
			t.Errorf("L09 行 ext 缺列 %s: %s", k, extRaw)
		}
	}
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_audit_log WHERE action='ledger_l09_column_missing' AND target_id=?`, bizNo); n != 0 {
		t.Errorf("列齐不应产生 ledger_l09_column_missing 审计，实为 %d 条", n)
	}

	// ② 列缺（模拟生产者被绕过：fields 不带注入值）⇒ 审计 warn 出现
	bizNo2 := submitPCWithFields(t, svc, map[string]any{
		"contract_no": "CT-2609-0001", // 只有 user 字段，注入键全缺
	})
	if err := svc.Approve(ctx, bizNo2, taskFor(t, db, bizNo2, "ou_x").TaskID, "ou_x", "同意", nil); err != nil {
		t.Fatal(err)
	}
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_audit_log WHERE action='ledger_l09_column_missing' AND target_id=?`, bizNo2); n == 0 {
		t.Error("列缺必须留 ledger_l09_column_missing 告警审计（否则缺口静默 —— B47 同族）")
	}
	if instOf(t, db, bizNo2).Status != flow.InstanceApproved {
		t.Error("告警不中断终态（else=告警，不是拒绝）")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
