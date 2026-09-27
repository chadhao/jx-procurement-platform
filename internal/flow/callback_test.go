package flow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// TestCallbackTokenAndIdempotency 回调：token 校验 + 幂等（重复回调不二次推进）。
func TestCallbackTokenAndIdempotency(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	svc.SetCallbackAdvancer(func(ctx context.Context, req flow.CallbackRequest) error {
		return svc.Approve(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason)
	})

	// ① 非法 token → 可见地拒绝。
	if _, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "bad-token", BizNo: bizNo, TaskID: m1.TaskID, OpType: "APPROVE", OperatorOpenID: "ou_m1",
	}); !errors.Is(err, flow.ErrInvalidToken) {
		t.Fatalf("非法 token 应 ErrInvalidToken，实际: %v", err)
	}

	// ② 合法首次 → 受理 + 推进一次（实例 APPROVED）。
	res, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: "APPROVE", OperatorOpenID: "ou_m1",
	})
	if err != nil {
		t.Fatalf("首次回调失败: %v", err)
	}
	if !res.Accepted || res.Duplicate {
		t.Fatalf("首次回调 = %+v, 期望 {Accepted:true Duplicate:false}", res)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Fatalf("首次回调后实例 = %s, 期望 APPROVED", got)
	}

	// ③ 重复回调 → 幂等（Duplicate=true），不二次推进。
	res2, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: "APPROVE", OperatorOpenID: "ou_m1",
	})
	if err != nil {
		t.Fatalf("重复回调不应报错: %v", err)
	}
	if !res2.Duplicate {
		t.Errorf("重复回调应 Duplicate=true，实际 %+v", res2)
	}
	// 留痕仅 1 条 APPROVE（幂等键 (biz_no,task_id,op_type)）。
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_flow_op_log WHERE biz_no=? AND task_id=? AND op_type='APPROVE'`, bizNo, m1.TaskID); n != 1 {
		t.Errorf("APPROVE 留痕 = %d, 期望 1（幂等）", n)
	}
}

// TestCallbackRejectsBadOp 非法操作类型 → 被拒（不静默落盘）。
func TestCallbackRejectsBadOp(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if _, err := svc.HandleCallback(context.Background(), flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: "TRANSFER", OperatorOpenID: "ou_m1",
	}); !errors.Is(err, flow.ErrIllegalTransition) {
		t.Errorf("非法回调操作应 ErrIllegalTransition，实际: %v", err)
	}
}

// ---------- 回调入口「准入先于占键」+ 入口校验缺口（定案 #62） ----------
//
// 覆盖 4 条缺口，每条含负向断言（证明能红）：
//   (a) 空 operator → 显式拒绝（不得占键）；
//   (b) biz_no 不存在 → 可见的 4xx 语义（`ErrInvalidSubmit`），**非** 500；
//   (c) 报文 instance_code 与 biz_no 解析结果一致（防串单）；
//   (d) ★ 幂等键污染：准入失败的请求**不得**消耗幂等键（完整因果链断言）。

// prodAdvancer 复刻生产装配（cmd/jxapproval/bootstrap.go）：把回调里的**真实 operator**
// 透传给 `flow.Approve/Reject`（不自造、不传空）——保证用例与生产同一链路。
func prodAdvancer(svc *flow.Service) flow.Advancer {
	return func(ctx context.Context, req flow.CallbackRequest) error {
		if req.OpType == flow.OpReject {
			return svc.Reject(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason)
		}
		return svc.Approve(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason)
	}
}

// callbackKeyCount 统计某实例已落盘的回调类留痕（APPROVE/REJECT）条数。
//
// ★ 用途：断言「是否占用了幂等键」——0 条＝键未被占用。
func callbackKeyCount(t *testing.T, db *store.DB, bizNo string) int {
	t.Helper()
	logs, err := db.ListFlowOpLogs(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读取操作留痕失败: %v", err)
	}
	n := 0
	for _, lg := range logs {
		if lg.OpType == flow.OpApprove || lg.OpType == flow.OpReject {
			n++
		}
	}
	return n
}

// TestCallbackRejectsEmptyOperator (a)：回调缺 operator → 显式拒绝，且**不占键**、不推进。
func TestCallbackRejectsEmptyOperator(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	svc.SetCallbackAdvancer(prodAdvancer(svc))

	_, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: flow.OpApprove, OperatorOpenID: "",
	})
	if !errors.Is(err, flow.ErrInvalidSubmit) {
		t.Fatalf("空 operator 应 ErrInvalidSubmit，实际: %v", err)
	}
	// 可见失败：不得占键（无 APPROVE 留痕）。
	if n := callbackKeyCount(t, db, bizNo); n != 0 {
		t.Errorf("空 operator 回调不得占幂等键，实际留痕 %d 条", n)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("空 operator 后实例 = %s，期望 PENDING", got)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskPending {
		t.Errorf("空 operator 后任务 = %s，期望 PENDING", got)
	}
}

// TestCallbackUnknownBizNoIsNotServerError (b)：biz_no 不存在 → 可见的 4xx 语义
// （`ErrInvalidSubmit`，handler 映射 400），**不得**退化为 500。
func TestCallbackUnknownBizNoIsNotServerError(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	_, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: "PR-9999-0001", TaskID: "PR-9999-0001-n1-ou_m1-1-1",
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if !errors.Is(err, flow.ErrInvalidSubmit) {
		t.Fatalf("未知 biz_no 应 ErrInvalidSubmit（→4xx，非 500），实际: %v", err)
	}
	if errors.Is(err, flow.ErrInvalidToken) {
		t.Fatalf("未知 biz_no 不应被误判为 token 失败: %v", err)
	}
}

// TestCallbackInstanceCodeMismatchRejected (c)：报文 instance_code 与 biz_no 解析结果不一致 → 拒绝（防串单），
// 且**不占键**；一致则放行推进。
func TestCallbackInstanceCodeMismatchRejected(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	svc.SetCallbackAdvancer(prodAdvancer(svc))

	// 串单：biz_no 正确、instance_code 却是**别的单** → 拒绝。
	_, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, InstanceCode: "app:PR-8888-0001",
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if !errors.Is(err, flow.ErrInvalidSubmit) {
		t.Fatalf("instance_code 不一致应 ErrInvalidSubmit，实际: %v", err)
	}
	if n := callbackKeyCount(t, db, bizNo); n != 0 {
		t.Errorf("串单回调不得占幂等键，实际留痕 %d 条", n)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Errorf("串单回调后实例 = %s，期望 PENDING", got)
	}

	// 对照：instance_code 一致（app:{biz_no}）→ 放行推进。
	res, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, InstanceCode: "app:" + bizNo,
		OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if err != nil {
		t.Fatalf("instance_code 一致应放行，实际: %v", err)
	}
	if res.Duplicate {
		t.Fatalf("首次一致回调不应被判 Duplicate")
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("一致回调后实例 = %s，期望 APPROVED", got)
	}
}

// TestCallbackAdmissionDoesNotConsumeIdempotencyKey (d)：★ 幂等键污染的**完整因果链**。
//
//	非本人回调 → 被拒（准入失败） → 随后**同键的真实回调仍能推进**（未被判重复）。
//
// 若把身份鉴权留在 `act`（即 `recordCallback` 之后），则：
// ① 非本人回调先占键 (biz_no,task_id,APPROVE) → ② 本人回调 `INSERT OR IGNORE` 命中 → Duplicate=true → 静默不推进。
// 本用例在缺陷版本下**必然变红**（见派单「证明能红」）。
func TestCallbackAdmissionDoesNotConsumeIdempotencyKey(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitOneNode(t, svc, "ou_m1")
	m1 := taskFor(t, db, bizNo, "ou_m1")
	svc.SetCallbackAdvancer(prodAdvancer(svc))

	// ① 非本人回调：必须被拒（准入失败）。
	if _, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: flow.OpApprove, OperatorOpenID: "ou_evil",
	}); !errors.Is(err, flow.ErrNotAssignee) {
		t.Fatalf("非本人回调应 ErrNotAssignee，实际: %v", err)
	}
	// ★ 关键：被拒回调**不得占键**。
	if n := callbackKeyCount(t, db, bizNo); n != 0 {
		t.Fatalf("被拒（准入失败）回调不得消耗幂等键，实际留痕 %d 条", n)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstancePending {
		t.Fatalf("被拒回调后实例 = %s，期望 PENDING", got)
	}

	// ② 随后**同键的真实回调**（本人）：必须仍能推进（未被判重复）。
	res, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if err != nil {
		t.Fatalf("同键真实回调应成功，实际: %v", err)
	}
	if res.Duplicate {
		t.Fatalf("同键真实回调被判 Duplicate —— 幂等键已被前一次被拒回调污染（静默卡住）")
	}
	if !res.Accepted {
		t.Fatalf("同键真实回调应 Accepted=true")
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Fatalf("同键真实回调后实例 = %s，期望 APPROVED（已真正推进）", got)
	}
	if got := taskFor(t, db, bizNo, "ou_m1").Status; got != flow.TaskApproved {
		t.Fatalf("同键真实回调后任务 = %s，期望 APPROVED", got)
	}

	// ③ 真去重仍生效：再重发同一真实回调 → Duplicate=true（幂等 no-op）。
	res2, err := svc.HandleCallback(ctx, flow.CallbackRequest{
		Token: "tok-pr", BizNo: bizNo, TaskID: m1.TaskID, OpType: flow.OpApprove, OperatorOpenID: "ou_m1",
	})
	if err != nil {
		t.Fatalf("重复回调不应报错，实际: %v", err)
	}
	if !res2.Duplicate || !res2.Accepted {
		t.Fatalf("重复回调应 Accepted=true, Duplicate=true，实际: %+v", res2)
	}
	if n := callbackKeyCount(t, db, bizNo); n != 1 {
		t.Errorf("本人成功回调应恰 1 条 APPROVE 留痕，实际 %d 条", n)
	}
}
