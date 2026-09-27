package flow_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
)

// repair_test.go —— #69 ② 的**恢复探针**与**幂等探针**（领域级）。
//
// 制造「回调已落盘、但任务未推进」（advancer 失败）→ 跑修复循环 → 断言真正推进；
// 再跑一次 → 断言无行可修、状态不变（连跑不双推进）。

// TestRepairPendingApprovalsRedrivesStuckCallback —— 恢复 + 幂等探针。
func TestRepairPendingApprovalsRedrivesStuckCallback(t *testing.T) {
	ctx := context.Background()
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")

	// —— 制造「已落盘、未推进」：advancer 失败（recordCallback 已写 op_log，act 未跑）——
	svc.SetCallbackAdvancer(func(ctx context.Context, req flow.CallbackRequest) error {
		return errors.New("探针：模拟推进失败")
	})
	res, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if err == nil || !res.Accepted {
		t.Fatalf("应「已受理但推进失败」，实际 res=%+v err=%v", res, err)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskPending {
		t.Fatalf("advancer 失败后任务应仍 PENDING（= 卡住），实际 %s", got)
	}
	if n := callbackKeyCount(t, db, bizNo); n != 1 {
		t.Fatalf("已落盘应恰 1 条 APPROVE 留痕，实际 %d", n)
	}

	// —— 恢复探针：跑一次修复循环 → 任务/实例真正推进 ——
	rep1, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("修复循环失败: %v", err)
	}
	if rep1.Scanned != 1 || rep1.Repaired != 1 || rep1.Failed != 0 {
		t.Fatalf("修复循环应 scanned=1 repaired=1 failed=0，实际 %+v", rep1)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskApproved {
		t.Fatalf("修复后任务应 APPROVED，实际 %s", got)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Fatalf("修复后实例应 APPROVED，实际 %s", got)
	}
	// 留痕仍只 1 条（`act` 的 INSERT OR IGNORE 命中，不新增第二行）。
	if n := callbackKeyCount(t, db, bizNo); n != 1 {
		t.Fatalf("修复后 APPROVE 留痕应仍为 1，实际 %d", n)
	}

	// —— 幂等探针：再跑一次 → 无行可修、状态不变（连跑不双推进）——
	rep2, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("第二次修复循环失败: %v", err)
	}
	if rep2.Scanned != 0 || rep2.Repaired != 0 {
		t.Fatalf("第二次应无行可修（scanned=0 repaired=0），实际 %+v", rep2)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskApproved {
		t.Fatalf("幂等：任务状态不应再变，实际 %s", got)
	}
	if n := callbackKeyCount(t, db, bizNo); n != 1 {
		t.Fatalf("幂等：APPROVE 留痕不应新增，实际 %d", n)
	}
}

// TestRepairPendingApprovalsRoundAligned —— ★ 按 `round` 对齐的**负向断言**（防重驱动错轮）。
//
// 构造（两节点单据，使实例保持非终态）：
//  1. n1（ou_m1）正常审批通过（round=1 留痕），n2（ou_m2）被释放，实例仍 PENDING；
//  2. 模拟「回退复用同一 task_id」：把 n1 重置为「第 2 轮 PENDING + RELEASED」。
//
// 此时**上一轮的旧留痕**（n1 round=1）**不得**被当作「本轮的待推进」而重驱动 ——
// 即 `o.round == t.round` 判据必须生效（否则会把回退后的新轮次误推进）。
func TestRepairPendingApprovalsRoundAligned(t *testing.T) {
	ctx := context.Background()
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	bizNo := submitTwoNodes(t, svc, "ou_m1", "ou_m2")
	m1 := taskFor(t, db, bizNo, "ou_m1")

	// 1) n1 正常通过（round=1 留痕）。
	svc.SetCallbackAdvancer(prodAdvancer(svc))
	if _, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	}); err != nil {
		t.Fatalf("n1 正常回调失败: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskApproved {
		t.Fatalf("n1 应 APPROVED，实际 %s", got)
	}

	// 2) 模拟「回退复用同一 task_id」：n1 重置为第 2 轮 PENDING + RELEASED（旧 round=1 留痕仍在）。
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		return db.ResetFlowTaskTx(ctx, tx, m1.TaskID, flow.TaskPending, flow.ReleaseReleased, 2)
	}); err != nil {
		t.Fatalf("重置 n1 失败: %v", err)
	}

	// 修复循环**不得**因旧留痕（round=1）而把第 2 轮任务误判为待推进。
	rep, err := svc.RepairPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("修复循环失败: %v", err)
	}
	if rep.Scanned != 0 {
		t.Fatalf("旧轮次留痕不得被选中（round 未对齐），实际 %+v", rep)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskPending {
		t.Fatalf("幂等：误推进不得发生，n1 应仍 PENDING，实际 %s", got)
	}
}
