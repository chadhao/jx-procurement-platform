package flow_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ---------------------------------------------------------------------------
// 独立复核两处回调准入缺口的探针（#62 残留半边 + 定义缺失落 500）
// ---------------------------------------------------------------------------

// TestCallbackHeldTaskRejectedWithoutKeyPollution ★ 缺口 1 探针：
// HELD（顺序会签尚未轮到）任务的抢跑回调必须**可见拒绝**（ErrTaskHeld）且**不占幂等键**；
// 该任务真正释放后，本人真实回调必须**立即推进**（不再依赖修复循环 ≤30s 兜底）。
//
// 缺陷机制（改前）：admitCallback 不校验 release_state —— 抢跑回调被放行（Accepted=true）
// 并落盘占键；任务真正释放后、本人真实回调被判 Duplicate=true ⇒ 不即时推进。
// ★ 与页面路径口径不一致：页面 Approve 对 HELD 任务得到可见的 409 ErrTaskHeld。
func TestCallbackHeldTaskRejectedWithoutKeyPollution(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	svc.SetCallbackAdvancer(prodAdvancer(svc))

	// 两节点链（submitTwoNode：n1 = ou_m1 → ou_m2 顺序会签，n2 = ou_gm）。
	// 提交后 ou_m1 RELEASED（可办理），ou_m2 HELD（未轮到）。
	bizNo := submitTwoNode(t, svc, db)
	m2 := taskFor(t, db, bizNo, "ou_m2")
	if m2.ReleaseState != flow.ReleaseHeld {
		t.Fatalf("前提不成立：ou_m2 release_state = %s, 期望 HELD", m2.ReleaseState)
	}

	// ① 抢跑回调（HELD 任务、本人操作）：必须可见拒绝、不占键。
	res, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m2.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m2",
	})
	if !errors.Is(err, flow.ErrTaskHeld) {
		t.Errorf("★ 缺口复现：HELD 任务抢跑回调未被拒绝（err=%v, res=%+v，期望 ErrTaskHeld）", err, res)
	}
	if res.Accepted {
		t.Errorf("★ 缺口复现：HELD 抢跑回调被受理（Accepted=true）——与页面路径 409 口径不一致")
	}
	// 准入失败不得占幂等键（#62）。
	if n := callbackKeyCount(t, db, bizNo); n != 0 {
		t.Errorf("★ 抢跑回调占用了幂等键（留痕 %d 条，期望 0）", n)
	}

	// ② 释放：ou_m1 应用内同意 → ou_m2 变为可办理（RELEASED）。
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_m1").TaskID, "ou_m1", "同意"); err != nil {
		t.Fatalf("ou_m1 同意失败: %v", err)
	}
	m2b := taskFor(t, db, bizNo, "ou_m2")
	if m2b.ReleaseState != flow.ReleaseReleased || m2b.Status != flow.TaskPending {
		t.Fatalf("释放前提不成立：ou_m2 = %s/%s, 期望 RELEASED/PENDING", m2b.ReleaseState, m2b.Status)
	}

	// ③ 本人真实回调：必须立即推进（非 Duplicate、任务 APPROVED、节点推进到 n2）。
	res2, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m2b.TaskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m2",
	})
	if err != nil {
		t.Fatalf("释放后本人真实回调失败: %v", err)
	}
	if res2.Duplicate {
		t.Errorf("★ 因果链复现：真实回调被判 Duplicate —— 幂等键已被抢跑回调污染，不即时推进（只能靠修复循环）")
	}
	if !res2.Accepted {
		t.Errorf("真实回调应 Accepted=true，实际 %+v", res2)
	}
	if got := taskFor(t, db, bizNo, "ou_m2").Status; got != flow.TaskApproved {
		t.Errorf("真实回调后 ou_m2 = %s, 期望 APPROVED（状态机已推进）", got)
	}
	if got := taskFor(t, db, bizNo, "ou_gm").ReleaseState; got != flow.ReleaseReleased {
		t.Errorf("真实回调后 n2（ou_gm）release_state = %s, 期望 RELEASED（已推进到下一节点）", got)
	}
}

// TestCallbackDefinitionMissingIsNot500 ★ 缺口 2 探针：
// 「实例在、审批定义缺失」的回调必须产生**可识别的领域哨兵**（ErrDefinitionMissing → 409），
// 绝不落 500/50000（500 会让飞书无限重试且掩盖真实原因）。
//
// 缺陷机制（改前）：verifyCallbackToken 里 GetApprovalDef 的 store.ErrNotFound 被包装成
// 普通 error（`flow: 回调定位审批定义失败`）⇒ 不命中任何哨兵 ⇒ callbackErrorStatus 落 500；
// ErrDefinitionMissing → 409 那行在回调路径**不可达**。
func TestCallbackDefinitionMissingIsNot500(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	// 构造「实例在、定义缺失」：直接落实例（绕过 Submit 的 S7 定义校验）+ 一条任务。
	const bizNo = "PR-2609-0009"
	if err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "app:" + bizNo, ApprovalCode: "code-ghost", DocType: "PR", BizNo: bizNo,
		Status: flow.InstancePending, Source: "flow", CreatedAt: flowAt, UpdatedAt: flowAt,
	}); err != nil {
		t.Fatalf("落实例失败: %v", err)
	}
	taskID := bizNo + "-n1-ou_x-1-1"
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		return db.UpsertFlowTaskTx(ctx, tx, &store.FlowTask{
			TaskID: taskID, BizNo: bizNo, NodeID: "n1", NodeName: "终审",
			NodeSeq: 1, Round: 1, AssigneeOpenID: "ou_x", AssigneeName: "审批人",
			Status: flow.TaskPending, ReleaseState: flow.ReleaseReleased, TaskOrder: 1,
			CreatedAt: flowAt, UpdatedAt: flowAt,
		})
	}); err != nil {
		t.Fatalf("落任务失败: %v", err)
	}

	_, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: taskID,
		OpType: flow.OpApprove, OperatorOpenID: "ou_x",
	})
	if !errors.Is(err, flow.ErrDefinitionMissing) {
		t.Errorf("★ 缺口复现：定义缺失回调未产生 ErrDefinitionMissing 哨兵（err=%v）"+
			"—— handler 将落 500/50000（未分类故障），飞书无限重试且掩盖真实原因", err)
	}
}
