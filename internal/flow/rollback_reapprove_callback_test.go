package flow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 回退后再次审批（回调路径）探针 —— 静默缺陷复现（README 头号红线「不报错但结果错/空」）。
//
// 缺陷机制（HEAD=98361ee 实测）：
//  1. Rollback 复用同一 task_id（ResetFlowTaskTx 只改 round，task_id 内嵌 round 不变）；
//  2. ux_flow_op_callback 幂等键只含 (biz_no, task_id, op_type) —— **不含 round**；
//  3. 回退后同 task_id 再次 APPROVE → INSERT OR IGNORE 命中既有键 → inserted=false
//     → HandleCallback 返回 Duplicate=true → **不调 advancer** → 状态机静默不推进，
//     而飞书侧收到 200。应用内路径（act 忽略返回值、继续 advanceTx）不受影响
//     ⇒ 同一逻辑动作两条路径行为不一致。
//
// 探针：submit → 第 1 轮 approve（走 HandleCallback，注入同步 advancer）→
// Rollback 回退到该节点 → 再次 approve（仍走 HandleCallback）。
// 断言：① 第二次**不是** Duplicate=true；② 实例继续推进（n2 被释放）；
// ③ t_flow_op_log 中该 task_id 有 **2 条** APPROVE 行，round 分别为 1 与 2。
func TestRollbackThenReapproveViaCallback(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	svc.SetCallbackAdvancer(prodAdvancer(svc))

	// 两节点链：n1（ou_m1）→ n2（ou_gm）。
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "主管", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_m1", Name: "李四"}}},
			{NodeID: "n2", NodeName: "总经理", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_gm", Name: "王五"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	// ── 第 1 轮：ou_m1 经回调 approve n1 ──
	m1 := taskFor(t, db, bizNo, "ou_m1")
	res1, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if err != nil {
		t.Fatalf("第 1 轮回调失败: %v", err)
	}
	if res1.Duplicate {
		t.Fatalf("第 1 轮回调被判 Duplicate（前提不成立）: %+v", res1)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskApproved {
		t.Fatalf("第 1 轮后 n1 任务 = %s, 期望 APPROVED", got)
	}

	// ── 回退：ou_gm（当前活动节点 n2 的待办人）回退到 n1 ──
	if err := svc.Rollback(ctx, bizNo, "ou_gm", "n1", "资料有误"); err != nil {
		t.Fatalf("回退失败: %v", err)
	}
	m1b := taskFor(t, db, bizNo, "ou_m1")
	if m1b.Status != flow.TaskPending || m1b.Round != 2 {
		t.Fatalf("回退后 n1 任务 = status=%s round=%d, 期望 PENDING/round=2", m1b.Status, m1b.Round)
	}

	// ── 第 2 轮：同一 task_id 再次经回调 approve ──
	res2, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1b.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			t.Fatalf("第 2 轮回调误判任务不存在: %v", err)
		}
		t.Fatalf("第 2 轮回调失败: %v", err)
	}
	// ① 幂等键必须按轮次区分：第二次不得被判 Duplicate。
	if res2.Duplicate {
		t.Fatalf("★ 缺陷复现：回退后同 task_id 再次 approve 被判 Duplicate=true（幂等键未含 round）" +
			"→ 回调路径不推进状态机，飞书侧仍收到 200（静默）")
	}
	if !res2.Accepted {
		t.Fatalf("第 2 轮回调应 Accepted=true，实际 %+v", res2)
	}

	// ② 实例必须继续推进：n1 再次通过 → n2 被释放为可办理。
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskApproved {
		t.Errorf("第 2 轮后 n1 任务 = %s, 期望 APPROVED", got)
	}
	gm := taskFor(t, db, bizNo, "ou_gm")
	if gm.Status != flow.TaskPending || gm.ReleaseState != "RELEASED" {
		t.Errorf("★ 状态机未推进：n2 任务 = status=%s release=%s, 期望 PENDING/RELEASED",
			gm.Status, gm.ReleaseState)
	}

	// ③ 留痕：该 task_id 应有 2 条 APPROVE 行，round 分别为 1 与 2。
	//    （raw SQL 读 round：修复前该列不存在，本探针在未修复代码上必然失败。）
	type opRow struct {
		round int
	}
	var rounds []opRow
	rows, err := db.QueryContext(ctx,
		`SELECT COALESCE(round,0) FROM t_flow_op_log
		 WHERE biz_no = ? AND task_id = ? AND op_type = 'APPROVE' ORDER BY op_id`,
		bizNo, m1.TaskID)
	if err != nil {
		t.Fatalf("读取操作留痕失败（修复前 round 列不存在即在此失败）: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r opRow
		if err := rows.Scan(&r.round); err != nil {
			t.Fatal(err)
		}
		rounds = append(rounds, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 2 {
		t.Fatalf("该 task_id 的 APPROVE 留痕 = %d 条, 期望 2 条（round 1 与 2）", len(rounds))
	}
	if rounds[0].round != 1 || rounds[1].round != 2 {
		t.Errorf("APPROVE 留痕 round 序列 = [%d, %d], 期望 [1, 2]", rounds[0].round, rounds[1].round)
	}
}
