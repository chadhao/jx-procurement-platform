package flow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
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
