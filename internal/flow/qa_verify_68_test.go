package flow_test

// qa_verify_68_test.go —— 独立验证者（qa-verify-68）验证测试**收编入库**版。
//
// 来源与依据：
//   - 首轮独立验证（qa-verify-68，worktree@e9f69fb）：#68（回调幂等键入 round）六项：
//     改前红 / 两写入点删参即红 / round 1→2→3 / 旧库升级 / 两路径一致性 / REJECT 路径。
//   - 二轮验证（worktree@5f8e35b）：#69 五项（契约/恢复/幂等/round 对齐/HELD·终态不动），
//     其间发现两处邻近缺口（HELD 准入不拦、定义缺失落 500），经 56d6138 修复。
//   - 本文件按 team-lead 指令收编「与 qa_verify_final 收编版（1b71ee5）不重叠」的三块：
//     ① round 1→2→3 连续升级；② 回调路径 vs 应用内路径最终状态 DeepEqual；
//     ③ ★ 原「HELD 抢跑回调占键后靠修复循环收敛」观察探针 —— 按定案 #79 翻转为守门断言
//       （56d6138 修复后：HELD 抢跑必须可见拒绝、不占键；释放后真实回调**立即**推进，
//        不再依赖修复循环的 ≤30s 延迟收敛 —— 即本守门覆盖「拒绝 + 不占键 + 即时推进」全弧）。
//
// ★ 命名 qav68 前缀，避免与 qa_verify_final_test.go（QAVF）及作者探针冲突。

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// qav68SubmitTwoNode 提交两节点链 n1(assignee) → n2(ou_gm)，返回 (bizNo, n1 任务 taskID)。
func qav68SubmitTwoNode(t *testing.T, db *store.DB, assignee string) (string, string) {
	t.Helper()
	svc := flow.New(db, "app")
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "主管", Seq: 1, Approvers: []flow.Approver{
				{OpenID: assignee, Name: "李四"}}},
			{NodeID: "n2", NodeName: "总经理", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_gm", Name: "王五"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo, taskFor(t, db, bizNo, assignee).TaskID
}

func qav68Callback(t *testing.T, svc *flow.Service, bizNo, taskID, operator, opType string) (flow.CallbackResult, error) {
	t.Helper()
	return svc.HandleCallback(context.Background(), flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: taskID,
		OpType: opType, OperatorOpenID: operator,
	})
}

// qav68ApproveRounds 某 task_id 的 APPROVE 留痕 round 序列（按 op_id 升序）。
// ★ 为什么重要：round 序列是「幂等键按轮次区分（0011）」的直接可观测证据。
func qav68ApproveRounds(t *testing.T, db *store.DB, bizNo, taskID string) []int {
	t.Helper()
	logs, err := db.ListFlowOpLogs(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读取留痕失败: %v", err)
	}
	var rounds []int
	for _, lg := range logs {
		if lg.OpType == flow.OpApprove && lg.TaskID == taskID {
			rounds = append(rounds, lg.Round)
		}
	}
	return rounds
}

// TestQAV68RoundEscalation_ThreeRoundsAllAdvance round 1→2→3：
// 同一 task 连续两次 Rollback，每次经回调审批都必须推进，且留痕恰 3 条、round=[1,2,3]。
// ★ 为什么重要：若第 3 轮被前两轮的键挡住（幂等键未按 round 区分），即静默不推进复发。
func TestQAV68RoundEscalation_ThreeRoundsAllAdvance(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	svc.SetCallbackAdvancer(prodAdvancer(svc))
	ctx := context.Background()
	bizNo, m1ID := qav68SubmitTwoNode(t, db, "ou_m1")

	for wantRound := 1; wantRound <= 3; wantRound++ {
		res, err := qav68Callback(t, svc, bizNo, m1ID, "ou_m1", flow.OpApprove)
		if err != nil || res.Duplicate || !res.Accepted {
			t.Fatalf("第 %d 轮回调异常: res=%+v err=%v（Duplicate=true＝静默缺陷回归）", wantRound, res, err)
		}
		if wantRound < 3 {
			gm := taskFor(t, db, bizNo, "ou_gm")
			if gm.Status != flow.TaskPending || gm.ReleaseState != "RELEASED" {
				t.Fatalf("第 %d 轮审批后状态机未推进: n2 = %s/%s", wantRound, gm.Status, gm.ReleaseState)
			}
			if err := svc.Rollback(ctx, bizNo, "ou_gm", "n1", "退回重审"); err != nil {
				t.Fatalf("第 %d 次回退失败: %v", wantRound, err)
			}
		}
		rounds := qav68ApproveRounds(t, db, bizNo, m1ID)
		if len(rounds) != wantRound {
			t.Fatalf("第 %d 轮后 APPROVE 留痕 = %v, 期望恰 %d 条", wantRound, rounds, wantRound)
		}
		for i, r := range rounds {
			if r != i+1 {
				t.Fatalf("APPROVE 留痕 round 序列 = %v, 期望 [1..%d]", rounds, wantRound)
			}
		}
	}
}

// qav68Outcome 两路径运行结果快照（可比较）。
type qav68Outcome struct {
	m1Status    string
	m1Round     int
	gmStatus    string
	gmRelease   string
	instStatus  string
	approveRows []int
}

// TestQAV68PathConsistency_CallbackVsInApp_DeepEqual 同一场景（审批→回退→再审批）
// 分别走回调路径（HandleCallback）与应用内路径（Approve），最终状态必须一致。
// ★ 为什么重要：同一逻辑动作两条路径行为不一致＝口径分裂（docs/11 R11 头号禁区）。
func TestQAV68PathConsistency_CallbackVsInApp_DeepEqual(t *testing.T) {
	run := func(t *testing.T, viaCallback bool) qav68Outcome {
		db := newFlowDB(t)
		svc := flow.New(db, "app")
		svc.SetCallbackAdvancer(prodAdvancer(svc))
		ctx := context.Background()
		bizNo, m1ID := qav68SubmitTwoNode(t, db, "ou_m1")
		if viaCallback {
			if _, err := qav68Callback(t, svc, bizNo, m1ID, "ou_m1", flow.OpApprove); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := svc.Approve(ctx, bizNo, m1ID, "ou_m1", "", nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := svc.Rollback(ctx, bizNo, "ou_gm", "n1", "资料有误"); err != nil {
			t.Fatal(err)
		}
		if viaCallback {
			if _, err := qav68Callback(t, svc, bizNo, m1ID, "ou_m1", flow.OpApprove); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := svc.Approve(ctx, bizNo, m1ID, "ou_m1", "", nil); err != nil {
				t.Fatal(err)
			}
		}
		m1 := taskFor(t, db, bizNo, "ou_m1")
		gm := taskFor(t, db, bizNo, "ou_gm")
		inst, err := db.GetInstanceByBizNo(ctx, bizNo)
		if err != nil {
			t.Fatal(err)
		}
		return qav68Outcome{
			m1Status: m1.Status, m1Round: m1.Round,
			gmStatus: gm.Status, gmRelease: gm.ReleaseState,
			instStatus: inst.Status, approveRows: qav68ApproveRounds(t, db, bizNo, m1ID),
		}
	}

	cb := run(t, true)
	ia := run(t, false)
	if !reflect.DeepEqual(cb, ia) {
		t.Fatalf("两路径结果不一致：\n回调路径  = %+v\n应用内路径 = %+v", cb, ia)
	}
	// 共同基线：n1 APPROVED/round=2、n2 释放待办、恰 2 条 APPROVE（round 1、2）。
	if cb.m1Status != flow.TaskApproved || cb.m1Round != 2 {
		t.Fatalf("n1 = %s/round=%d, 期望 APPROVED/2", cb.m1Status, cb.m1Round)
	}
	if cb.gmStatus != flow.TaskPending || cb.gmRelease != "RELEASED" {
		t.Fatalf("n2 = %s/%s, 期望 PENDING/RELEASED", cb.gmStatus, cb.gmRelease)
	}
	if !reflect.DeepEqual(cb.approveRows, []int{1, 2}) {
		t.Fatalf("APPROVE 留痕 round = %v, 期望 [1 2]（回调路径不得多出重复行）", cb.approveRows)
	}
}

// TestQAV68HeldCallbackGate_FullArcVisibleRejectThenImmediateAdvance
// ★ 定案 #79 翻转守门（原观察探针 TestQA69_HeldCallbackPollutesKeyThenRepairHeals）：
//
//	翻转前（56d6138 之前）：HELD 抢跑回调 Accepted=true 落盘占键 → 释放后真实回调
//	  判 Duplicate=true 不即时推进 → 只能靠修复循环 ≤30s 收敛（断言「缺口存在」）。
//	翻转后（本用例，HEAD=56d6138+）：三段守门断言 ——
//	  ① HELD 抢跑回调必须**可见拒绝**（errors.Is(err, ErrTaskHeld)）且 Accepted=false；
//	  ② **不占幂等键**：该任务 APPROVE 留痕 == 0 条；
//	  ③ 任务释放后，本人真实回调**立即**推进（Accepted 且非 Duplicate，任务 APPROVED），
//	    且此后修复循环扫描 Scanned=0（不欠修复循环的债）。
//
// ★ 改前红证据：本用例在 56d6138~1（未修准入）上 FAIL（①②均不成立），见提交消息。
func TestQAV68HeldCallbackGate_FullArcVisibleRejectThenImmediateAdvance(t *testing.T) {
	ctx := context.Background()
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	svc.SetCallbackAdvancer(prodAdvancer(svc))

	// n1 两人顺序释放（ou_a 先、ou_b 后），n2 单人。
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "会签", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_a", Name: "A"}, {OpenID: "ou_b", Name: "B"}}},
			{NodeID: "n2", NodeName: "总经理", Seq: 2, Approvers: []flow.Approver{
				{OpenID: "ou_gm", Name: "王五"}}},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	tb := taskFor(t, db, bizNo, "ou_b")
	if tb.ReleaseState != "HELD" {
		t.Fatalf("前提不成立: ou_b release=%s, 期望 HELD", tb.ReleaseState)
	}

	// ① HELD 抢跑回调 → 可见拒绝（ErrTaskHeld），绝不 Accepted。
	res, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: tb.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_b",
	})
	if !errors.Is(err, flow.ErrTaskHeld) {
		t.Fatalf("★ HELD 抢跑回调必须可见拒绝 ErrTaskHeld, 实际 res=%+v err=%v", res, err)
	}
	if res.Accepted {
		t.Fatalf("★ HELD 抢跑回调被受理（占键缺口回归）: %+v", res)
	}

	// ② 不占幂等键：该任务的 APPROVE 留痕必须为 0 条。
	if rows := qav68ApproveRounds(t, db, bizNo, tb.TaskID); len(rows) != 0 {
		t.Fatalf("★ 准入失败仍占幂等键（#62 顺序纪律回归）: APPROVE 留痕 %d 条", len(rows))
	}

	// ③ 释放后真实回调立即推进：ou_a 审批 → ou_b RELEASED → ou_b 真实回调。
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_a").TaskID, "ou_a", "", nil); err != nil {
		t.Fatal(err)
	}
	tb2 := taskFor(t, db, bizNo, "ou_b")
	if tb2.ReleaseState != "RELEASED" || tb2.Status != flow.TaskPending {
		t.Fatalf("前提不成立: ou_b = %s/%s, 期望 PENDING/RELEASED", tb2.Status, tb2.ReleaseState)
	}
	res3, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: tb.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_b",
	})
	if err != nil {
		t.Fatalf("释放后真实回调失败: %v", err)
	}
	if res3.Duplicate || !res3.Accepted {
		t.Fatalf("★ 释放后真实回调被判 Duplicate/未受理（键污染残留）: %+v", res3)
	}
	if got := taskFor(t, db, bizNo, "ou_b").Status; got != flow.TaskApproved {
		t.Fatalf("★ 释放后真实回调未即时推进: ou_b = %s（修复循环依赖回归）", got)
	}

	// 收尾：全弧走完不欠修复循环的债。
	rep, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 0 {
		t.Fatalf("真实回调已即时推进后仍有卡住行被扫出: %+v", rep)
	}
}
