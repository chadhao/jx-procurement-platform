package flow_test

// qa_verify_final_test.go —— 独立验证者（qa-verify-final）验证测试**收编入库**版。
//
// 来源与依据：
//   - 首轮独立验证（qa-verify-final，HEAD=25066a0，独立 worktree jx-qa2）：#69 ①②③④⑤
//     自建场景全 PASS（REJECT 路径恢复/幂等、真实 Rollback 构造 round 错配、终态边界）。
//   - ★ ⑤a 已按 team-lead 指令**翻转**为负向回归断言：原「观察缺口」（HELD 任务带留痕——
//     抢跑回调准入通过落盘占键）在 56d6138（#62 残留半边缺口 1）修复后**必须不再发生**。
//     翻转判据：缺口复现测试在缺口修好后失败 ≠ 测试错了，而是它该翻转成守门断言。
//   - 新增定义缺失哨兵回归（56d6138 缺口 2）：定案 #62（准入先于占键）、#69（落盘即 200）、
//     #77（改前红探针在 worktree 取证）。
//
// ★ 刻意与作者探针（callback_admission_gaps_test.go）不同构：
//   - HELD 用**跨节点** HELD（n2 未轮到）而非同节点顺序会签，且含修复循环负向断言；
//   - 定义缺失用「提交后定义被删」构造（作者用「直接落实例绕过 S7」）。

import (
	"context"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// failAdvancer 必定失败的推进端口（模拟「落盘成功、推进失败」）。
func failAdvancer(msg string) flow.Advancer {
	return func(ctx context.Context, req flow.CallbackRequest) error { return errors.New(msg) }
}

// opLogCount 某 (biz_no, task_id, op_type) 的留痕行数。
func opLogCount(t *testing.T, db *store.DB, bizNo, taskID, opType string) int {
	t.Helper()
	return storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_flow_op_log WHERE biz_no=? AND task_id=? AND op_type=?`,
		bizNo, taskID, opType)
}

// TestQAVF69RejectRepairRecoversThenIdempotent —— #69 ② 恢复 + ③ 幂等（REJECT 路径独立构景）。
//
// 为什么重要：修复循环是「落盘即 200」（#69 ①）的必然兜底；若它失守，回调落盘后
// 推进失败的任务将**静默卡死**（飞书已收 200、重发判 Duplicate）——本仓库头号红线。
func TestQAVF69RejectRepairRecoversThenIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")

	// ── 制造「已落盘、未推进」（REJECT 回调 + 必败 advancer）──
	svc.SetCallbackAdvancer(failAdvancer("qa-vf: 模拟推进失败"))
	res, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID,
		OpType: flow.OpReject, OperatorOpenID: "ou_m1", Reason: "不符合要求",
	})
	if !res.Accepted || err == nil {
		t.Fatalf("REJECT 已落盘应 Accepted=true 且 err 仅透出，实际 res=%+v err=%v", res, err)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskPending {
		t.Fatalf("advancer 必败后任务应仍 PENDING（= 卡住），实际 %s", got)
	}
	if n := opLogCount(t, db, bizNo, m1.TaskID, "REJECT"); n != 1 {
		t.Fatalf("落盘 REJECT 留痕应恰 1 条，实际 %d", n)
	}

	// ── ② 恢复：跑一次修复循环 → 任务/实例真正推进为 REJECTED ──
	rep1, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("修复循环失败: %v", err)
	}
	if rep1.Scanned != 1 || rep1.Repaired != 1 || rep1.Failed != 0 {
		t.Fatalf("② 恢复应 scanned=1 repaired=1 failed=0，实际 %+v", rep1)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskRejected {
		t.Fatalf("② 修复后任务应 REJECTED，实际 %s", got)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceRejected {
		t.Fatalf("② 修复后实例应 REJECTED，实际 %s", got)
	}

	// ── ③ 幂等：再跑一次 → Scanned=0、状态不变、留痕不新增 ──
	rep2, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("第二次修复循环失败: %v", err)
	}
	if rep2.Scanned != 0 || rep2.Repaired != 0 {
		t.Fatalf("③ 第二次应无行可修，实际 %+v", rep2)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskRejected {
		t.Fatalf("③ 状态不应再变，实际 %s", got)
	}
	if n := opLogCount(t, db, bizNo, m1.TaskID, "REJECT"); n != 1 {
		t.Fatalf("③ 留痕不应新增（应仍 1 条），实际 %d", n)
	}
}

// TestQAVF69RepairSkipsStaleRoundAfterRealRollback —— ④ round 对齐负向断言（真实 Rollback 构景）。
//
// 为什么重要：若修复循环不按 round 对齐，回退后的新轮次会被上一轮旧留痕**误推进**
// （跳过本轮真实审批）——0011 round 入键能力在修复循环侧的用途。
func TestQAVF69RepairSkipsStaleRoundAfterRealRollback(t *testing.T) {
	ctx := context.Background()
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	svc.SetCallbackAdvancer(prodAdvancer(svc))
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_gm")
	m1 := taskFor(t, db, bizNo, "ou_m1")

	// 第 1 轮：n1（ou_m1）经真实回调通过 → 留下 round=1 的 APPROVE 留痕。
	if _, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	}); err != nil {
		t.Fatalf("第 1 轮回调失败: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskApproved {
		t.Fatalf("前提不成立：第 1 轮后 n1 = %s, 期望 APPROVED", got)
	}

	// 真实回退：ou_gm 回退到 n1 → 同一 task_id 复用、round=2、任务回到 PENDING。
	if err := svc.Rollback(ctx, bizNo, "ou_gm", "n1", "资料有误"); err != nil {
		t.Fatalf("回退失败: %v", err)
	}
	m1b := taskFor(t, db, bizNo, "ou_m1")
	if m1b.TaskID != m1.TaskID || m1b.Status != flow.TaskPending || m1b.Round != 2 {
		t.Fatalf("前提不成立：回退后 n1 = task_id=%s status=%s round=%d, 期望同 task_id/PENDING/round=2",
			m1b.TaskID, m1b.Status, m1b.Round)
	}
	if n := opLogCount(t, db, bizNo, m1.TaskID, "APPROVE"); n != 1 {
		t.Fatalf("前提不成立：旧轮 APPROVE 留痕应恰 1 条，实际 %d", n)
	}

	// ④ 核心负向断言：旧轮（round=1）留痕不得被当作本轮（round=2）的待推进重驱动。
	rep, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("修复循环失败: %v", err)
	}
	if rep.Scanned != 0 {
		t.Fatalf("★ ④ 失守：旧轮留痕被选中重驱动（round 未对齐），实际 %+v", rep)
	}
	m1c := taskFor(t, db, bizNo, "ou_m1")
	if m1c.Status != flow.TaskPending || m1c.Round != 2 {
		t.Fatalf("★ ④ 失守：回退后任务被旧轮留痕推进，实际 status=%s round=%d", m1c.Status, m1c.Round)
	}
}

// TestQAVFHeldTaskCallbackVisiblyRejectedNoKeyPollution —— ⑤a 翻转后的**守门断言**（56d6138 缺口 1）。
//
// ★ 原为「观察缺口存在」的探针（HELD 任务带留痕：抢跑回调准入通过落盘占键）——
//
//	56d6138 修复后翻转为负向回归：HELD 任务的抢跑回调必须**可见拒绝**（ErrTaskHeld、
//	Accepted=false）且**不占幂等键**（#62：准入先于占键）；修复循环不得扫到任何行；
//	任务真正释放后，本人真实回调必须**立即推进**（不依赖修复循环 ≤30s 兜底，
//	与页面路径 409 口径一致）。
//
// 为什么重要：若此断言失守，抢跑回调会污染幂等键 → 真实回调判 Duplicate →
// 不即时推进（静默卡死 30s）——正是本断言守住的「缺口不再回来」守卫。
func TestQAVFHeldTaskCallbackVisiblyRejectedNoKeyPollution(t *testing.T) {
	ctx := context.Background()
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	svc.SetCallbackAdvancer(prodAdvancer(svc))

	// 两节点链：n1（ou_m1）→ n2（ou_gm）；提交后 ou_gm 跨节点 HELD（未轮到）。
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_gm")
	gm := taskFor(t, db, bizNo, "ou_gm")
	if gm.ReleaseState != flow.ReleaseHeld {
		t.Fatalf("前提不成立：n2（ou_gm）release_state = %s, 期望 HELD", gm.ReleaseState)
	}

	// ① 抢跑回调（HELD 任务、本人操作）：可见拒绝、不占键。
	res, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: gm.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_gm",
	})
	if !errors.Is(err, flow.ErrTaskHeld) {
		t.Fatalf("★ 守门失守：HELD 抢跑回调应 ErrTaskHeld，实际 err=%v res=%+v", err, res)
	}
	if res.Accepted {
		t.Errorf("★ 守门失守：HELD 抢跑回调被受理（Accepted=true）——与页面路径 409 口径不一致")
	}
	// #62：准入失败不得占幂等键（留痕 0 条）。
	if n := opLogCount(t, db, bizNo, gm.TaskID, "APPROVE"); n != 0 {
		t.Errorf("★ 守门失守：抢跑回调占用幂等键（留痕 %d 条，期望 0）", n)
	}

	// ② 修复循环不得扫到任何行（抢跑回调未落盘 → 无「已留痕未推进」）。
	rep, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("修复循环失败: %v", err)
	}
	if rep.Scanned != 0 {
		t.Errorf("★ 守门失守：抢跑回调落盘后被修复循环选中，实际 %+v", rep)
	}

	// ③ 释放：ou_m1 经回调通过 → ou_gm 变 RELEASED。
	if _, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: taskFor(t, db, bizNo, "ou_m1").TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	}); err != nil {
		t.Fatalf("n1 回调失败: %v", err)
	}
	gmB := taskFor(t, db, bizNo, "ou_gm")
	if gmB.ReleaseState != flow.ReleaseReleased || gmB.Status != flow.TaskPending {
		t.Fatalf("释放前提不成立：ou_gm = %s/%s, 期望 RELEASED/PENDING", gmB.ReleaseState, gmB.Status)
	}

	// ④ 本人真实回调：立即推进（非 Duplicate、不依赖修复循环）。
	res2, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: gmB.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_gm",
	})
	if err != nil {
		t.Fatalf("释放后本人真实回调失败: %v", err)
	}
	if res2.Duplicate {
		t.Errorf("★ 因果链复现：真实回调被判 Duplicate —— 幂等键被抢跑回调污染（只能靠修复循环兜底）")
	}
	if !res2.Accepted {
		t.Errorf("真实回调应 Accepted=true，实际 %+v", res2)
	}
	if got := taskFor(t, db, bizNo, "ou_gm").Status; got != flow.TaskApproved {
		t.Errorf("真实回调后 ou_gm = %s, 期望 APPROVED（状态机已立即推进）", got)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("全链通过后实例 = %s, 期望 APPROVED", got)
	}
}

// TestQAVF69RepairSkipsTerminalInstance —— ⑤b 实例已终态 → 修复循环必须不动。
//
// 为什么重要：终态实例下残留的未兑现留痕若被重驱动，等于**死人复活**（终态后再次迁移）。
func TestQAVF69RepairSkipsTerminalInstance(t *testing.T) {
	ctx := context.Background()
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")

	// 制造「已落盘、未推进」。
	svc.SetCallbackAdvancer(failAdvancer("qa-vf: 模拟推进失败"))
	if _, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	}); err == nil {
		t.Fatalf("必败 advancer 应透出 err")
	}
	// 强制实例终态（模拟竞态：他路已把实例推到终态）。
	if _, err := db.ExecContext(ctx,
		`UPDATE t_instance SET status=? WHERE biz_no=?`, flow.InstanceApproved, bizNo); err != nil {
		t.Fatalf("置实例终态失败: %v", err)
	}

	// ⑤b 核心断言：终态实例下的残留留痕不得被重驱动。
	rep, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("修复循环失败: %v", err)
	}
	if rep.Scanned != 0 {
		t.Fatalf("★ ⑤b 失守：终态实例的留痕被扫描/重驱动，实际 %+v", rep)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskPending {
		t.Fatalf("★ ⑤b 失守：终态实例下任务被改动，实际 %s", got)
	}
}

// TestQAVFDefinitionMissingYieldsSentinel —— 56d6138 缺口 2 回归断言（独立构景：提交后定义被删）。
//
// 为什么重要：定义缺失若不升格为 ErrDefinitionMissing，会被包装成普通 error →
// 不命中任何哨兵 → handler 落 500/50000 → 飞书**无限重试**且掩盖真实原因（服务端配置问题
// 绝不该按「未分类故障」回 500）。
func TestQAVFDefinitionMissingYieldsSentinel(t *testing.T) {
	ctx := context.Background()
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")

	// 构造「实例在、定义缺失」：提交成功后删除定义（模拟服务端配置丢失）。
	if _, err := db.ExecContext(ctx, `DELETE FROM t_approval_def WHERE approval_code='code-pr'`); err != nil {
		t.Fatalf("删除定义失败: %v", err)
	}

	_, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if !errors.Is(err, flow.ErrDefinitionMissing) {
		t.Fatalf("★ 守门失守：定义缺失应产生 ErrDefinitionMissing 哨兵（→409），实际 err=%v "+
			"—— 无哨兵则 handler 落 500/50000，飞书无限重试且掩盖真实原因", err)
	}
	// 未受理 ⇒ 不得落盘占键。
	if n := opLogCount(t, db, bizNo, m1.TaskID, "APPROVE"); n != 0 {
		t.Errorf("定义缺失被拒后不得落盘占键（留痕 %d 条，期望 0）", n)
	}
}
