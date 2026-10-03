package flow_test

// N-036 缺口 5/6 验收：SS 节点时点字段的非空强制（tech_opinion @ node2 / pgm_final @ node4）。
//   ① 必填拦截（fields 非 nil 通道）且任务不推进、ext 无残留；
//   ② 带齐放行且系统带入（tech_opinion_by/at = 审批人/时刻）；
//   ③ nil 通道（飞书回调/repair）豁免不卡死；④ 非 SS / 其它节点 no-op。

import (
	"context"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
)

// submitSSChain 提交一张 SS，链上两节点＝tech_opinion（seq2 前先有 submit_reason）与 pgm_final。
func submitSSChain(t *testing.T, svc *flow.Service) string {
	t.Helper()
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "SS", ApprovalCode: "code-pr", // 复用预置 def（code-pr→PR，Submit 只查存在）
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "submit_reason", NodeName: "提交理由", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_p", Name: "经办人"}}},
			{NodeID: "tech_opinion", NodeName: "技术意见", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_tech", Name: "质检技术部"}}},
			{NodeID: "tier_chain", NodeName: "分档审批", Seq: 3, Approvers: []flow.Approver{
				{OpenID: "ou_sup", Name: "主管"}}},
			{NodeID: "pgm_final", NodeName: "终审", Seq: 4, Approvers: []flow.Approver{
				{OpenID: "ou_pgm", Name: "项目总经理"}}},
			{NodeID: "ledger_and_report", NodeName: "台账报送", Seq: 5, Approvers: []flow.Approver{
				{OpenID: "ou_ops", Name: "运营"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

func TestSSTechOpinionRequiredAtNode(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSSChain(t, svc)
	// 先推进 seq1（submit_reason —— 非必填字段节点，nil 通道可过）
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_p").TaskID, "ou_p", "ok", nil); err != nil {
		t.Fatalf("推进 submit_reason: %v", err)
	}
	tk := taskFor(t, db, bizNo, "ou_tech")

	// ① 缺 tech_opinion ⇒ 拦（ErrInvalidNodeField），任务仍 PENDING、ext 无残留
	err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_tech", "同意", map[string]any{})
	if !errors.Is(err, flow.ErrInvalidNodeField) {
		t.Fatalf("缺技术意见应 ErrInvalidNodeField, 实际: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_tech").Status; got != "PENDING" {
		t.Errorf("拦截后任务状态 = %s, 期望 PENDING", got)
	}
	if _, has := extOf(t, db, bizNo)["tech_opinion"]; has {
		t.Error("拦截后 ext 不应有 tech_opinion")
	}

	// ② 带齐 ⇒ 放行 ＋ 系统带入 by/at
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_tech", "同意", map[string]any{
		"tech_opinion": "符合技术要求，同意使用",
	}); err != nil {
		t.Fatalf("带齐应放行: %v", err)
	}
	ext := extOf(t, db, bizNo)
	if ext["tech_opinion"] != "符合技术要求，同意使用" {
		t.Errorf("tech_opinion = %v", ext["tech_opinion"])
	}
	if ext["tech_opinion_by"] != "ou_tech" {
		t.Errorf("tech_opinion_by 应系统带入 ou_tech, 实为 %v", ext["tech_opinion_by"])
	}
	if v, _ := ext["tech_opinion_at"].(string); v == "" {
		t.Errorf("tech_opinion_at 应系统带入非空时刻: %v", ext)
	}
	if got := taskFor(t, db, bizNo, "ou_tech").Status; got != "APPROVED" {
		t.Errorf("放行后任务状态 = %s", got)
	}
}

func TestSSPgmFinalRequiredAndNilExempt(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSSChain(t, svc)

	// 推进到 pgm_final（approve 前两节点）
	for _, who := range []string{"ou_p", "ou_tech"} {
		if who == "ou_tech" {
			if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, who).TaskID, who, "ok",
				map[string]any{"tech_opinion": "符合"}); err != nil {
				t.Fatalf("推进 %s: %v", who, err)
			}
			continue
		}
		if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, who).TaskID, who, "ok", nil); err != nil {
			t.Fatalf("推进 %s: %v", who, err)
		}
	}
	// tier_chain（非必填字段节点）
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_sup").TaskID, "ou_sup", "ok", nil); err != nil {
		t.Fatalf("推进 tier_chain: %v", err)
	}

	tk := taskFor(t, db, bizNo, "ou_pgm")
	// ③ nil 通道豁免（回调/repair 形态）：不因缺 pgm_final_opinion 卡死
	//    —— 但 nil 不写入；此处先测 nil 豁免行为会把任务推过去导致②没得测，
	//    故顺序反过来：先测非 nil 拦、再带齐、nil 的豁免用独立单据。
	err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_pgm", "同意", map[string]any{})
	if !errors.Is(err, flow.ErrInvalidNodeField) {
		t.Fatalf("缺终审意见应拦, 实际: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_pgm").Status; got != "PENDING" {
		t.Errorf("拦截后 pgm_final 任务状态 = %s", got)
	}
	// ② 带齐放行
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_pgm", "同意", map[string]any{
		"pgm_final_opinion": "同意独家采购",
	}); err != nil {
		t.Fatalf("带齐应放行: %v", err)
	}
	if extOf(t, db, bizNo)["pgm_final_opinion"] != "同意独家采购" {
		t.Errorf("pgm_final_opinion 未落 ext: %v", extOf(t, db, bizNo))
	}

	// ③ nil 通道豁免：新单走到 pgm_final 后以 nil 同意 ⇒ 成功（不卡回调/repair）
	bizNo2 := submitSSChain(t, svc)
	for _, step := range []struct{ who, opinion string }{
		{"ou_p", ""}, {"ou_tech", "符合"}, {"ou_sup", ""},
	} {
		fields := map[string]any{}
		if step.opinion != "" {
			fields["tech_opinion"] = step.opinion
		}
		var f map[string]any
		if len(fields) > 0 {
			f = fields
		}
		if err := svc.Approve(ctx, bizNo2, taskFor(t, db, bizNo2, step.who).TaskID,
			step.who, "ok", f); err != nil {
			t.Fatalf("推进 %s: %v", step.who, err)
		}
	}
	if err := svc.Approve(ctx, bizNo2, taskFor(t, db, bizNo2, "ou_pgm").TaskID,
		"ou_pgm", "同意", nil); err != nil {
		t.Fatalf("nil 通道必须豁免（不卡回调/repair）: %v", err)
	}
	if got := taskFor(t, db, bizNo2, "ou_pgm").Status; got != "APPROVED" {
		t.Errorf("nil 豁免后任务状态 = %s", got)
	}
	if _, has := extOf(t, db, bizNo2)["pgm_final_opinion"]; has {
		t.Error("nil 通道不写入（豁免＝不校验也不落值）")
	}
}

// TestSSFrozenFieldsIgnoredOnOtherDocs 非 SS 单据同节点 id ⇒ no-op（不误伤）。
func TestSSFrozenFieldsIgnoredOnOtherDocs(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	// PR 单据上人为构造同名节点（node id=tech_opinion）—— 若 SS 逻辑误伤非 SS 单，
	// 空 opinion 的 PR 同意会被拦
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "tech_opinion", NodeName: "同名节点", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_x", Name: "某人"},
			}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_x").TaskID, "ou_x", "同意", nil); err != nil {
		t.Fatalf("PR 单据同名节点不应受 SS 强制约束: %v", err)
	}
}

// TestPCLedgerSubmitResubmitDateRequired N-036：PC×ledger_submit 节点必须登记
// resubmitted_to_group_at（rule：综合运营主管登记实际提交日期 —— 该节点 actor=ops_supervisor）。
// ★ section filled_at="node3…" 的 node3 部分**未拦**（spec 含糊处已回执请澄清，不抢跑）。
func TestPCLedgerSubmitResubmitDateRequired(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	amt := int64(500000)
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PC", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		AmountCents: &amt, Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "submit_change", NodeName: "变更提交", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_p", Name: "经办人"},
			}},
			{NodeID: "ledger_submit", NodeName: "台账报送", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_ops", Name: "运营主管"},
			}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_p").TaskID, "ou_p", "ok", nil); err != nil {
		t.Fatalf("推进 seq1: %v", err)
	}
	tk := taskFor(t, db, bizNo, "ou_ops")
	// 空登记日期 ⇒ 拦
	err = svc.Approve(ctx, bizNo, tk.TaskID, "ou_ops", "同意", map[string]any{})
	if !errors.Is(err, flow.ErrInvalidNodeField) {
		t.Fatalf("缺登记日期应拦, 实际: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_ops").Status; got != "PENDING" {
		t.Errorf("拦截后任务状态 = %s", got)
	}
	// 带齐 ⇒ 放行
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_ops", "同意", map[string]any{
		"resubmitted_to_group_at": "2026-10-03",
	}); err != nil {
		t.Fatalf("带齐应放行: %v", err)
	}
	if extOf(t, db, bizNo)["resubmitted_to_group_at"] != "2026-10-03" {
		t.Errorf("登记日期未落 ext: %v", extOf(t, db, bizNo))
	}
}
