package sync

import (
	"context"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// countingPusher 记录被要求重推的 biz_no（Repusher 测试替身）。
type countingPusher struct {
	calls []string
}

func (c *countingPusher) Push(_ context.Context, bizNo string) (feishu.PushResult, error) {
	c.calls = append(c.calls, bizNo)
	return feishu.PushResult{Pushed: true}, nil
}

func seedPendingInstance(t *testing.T, db *store.DB, code, bizNo string, updateTime int64) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.UpsertInstance(context.Background(), &store.Instance{
		InstanceCode: code, ApprovalCode: "ac", DocType: "PR", BizNo: bizNo,
		Status: "PENDING", Source: "flow", UpdateTime: updateTime,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed instance %s 失败: %v", code, err)
	}
}

// newRec 构造一个只对 approval_code="ac" 返回给定差异的审批对账器。
func newRec(t *testing.T, db *store.DB, pusher Repusher, states ...RemoteInstanceState) *ApprovalReconciler {
	t.Helper()
	checker := ExtSyncCheckerFunc(func(_ context.Context, code string) ([]RemoteInstanceState, error) {
		return states, nil
	})
	return NewApprovalReconciler(db, checker, pusher, nil, nil, nil)
}

// TestApprovalReconcilerDirectionJudgment 覆盖方向判断四分支（本器核心，S14 防倒退）：
//
//	① 我方领先（local.UpdateTime > remote）→ 重推；
//	② 飞书侧领先（local < remote）→ 禁止覆盖、跳过（stale_skip），不重推；
//	③ 方向不可判（remote.UpdateTime=0）→ 不盲推（undecidable）；
//	④ 平台有我方无 → missing（人工核对，不自动补）。
func TestApprovalReconcilerDirectionJudgment(t *testing.T) {
	db := storetest.NewDB(t)
	seedPendingInstance(t, db, "ac:x", "PR-1", 5) // 我方版本 = 5

	t.Run("①我方领先→重推", func(t *testing.T) {
		p := &countingPusher{}
		rep, err := newRec(t, db, p, RemoteInstanceState{InstanceID: "ac:x", UpdateTime: 3}).RunOnce(context.Background(), "ac")
		if err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if rep.Repushed != 1 || len(p.calls) != 1 || p.calls[0] != "PR-1" {
			t.Fatalf("我方领先应重推 1 次：rep=%+v calls=%v", rep, p.calls)
		}
		if rep.StaleSkip != 0 || rep.Undecidable != 0 || rep.Missing != 0 {
			t.Fatalf("不应有其他分支命中：%+v", rep)
		}
	})

	t.Run("②飞书侧领先→禁止覆盖", func(t *testing.T) {
		p := &countingPusher{}
		rep, err := newRec(t, db, p, RemoteInstanceState{InstanceID: "ac:x", UpdateTime: 9}).RunOnce(context.Background(), "ac")
		if err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if rep.StaleSkip != 1 || rep.Repushed != 0 || len(p.calls) != 0 {
			t.Fatalf("飞书侧领先必须跳过重推（S14）：rep=%+v calls=%v", rep, p.calls)
		}
	})

	t.Run("③方向不可判→不盲推", func(t *testing.T) {
		p := &countingPusher{}
		rep, err := newRec(t, db, p, RemoteInstanceState{InstanceID: "ac:x", UpdateTime: 0}).RunOnce(context.Background(), "ac")
		if err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if rep.Undecidable != 1 || rep.Repushed != 0 || len(p.calls) != 0 {
			t.Fatalf("方向不可判必须不盲推：rep=%+v calls=%v", rep, p.calls)
		}
	})

	t.Run("④平台有我方无→missing", func(t *testing.T) {
		p := &countingPusher{}
		rep, err := newRec(t, db, p, RemoteInstanceState{InstanceID: "ac:unknown", UpdateTime: 9}).RunOnce(context.Background(), "ac")
		if err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if rep.Missing != 1 || rep.Repushed != 0 {
			t.Fatalf("平台有我方无应计 missing 且不重推：%+v", rep)
		}
	})
}

// TestApprovalReconcilerConsecutiveAlert 连续 3 次不一致 → 告警一次（docs/02 §4.2）。
func TestApprovalReconcilerConsecutiveAlert(t *testing.T) {
	db := storetest.NewDB(t)
	seedPendingInstance(t, db, "ac:x", "PR-1", 5)
	rec := newRec(t, db, &countingPusher{}, RemoteInstanceState{InstanceID: "ac:x", UpdateTime: 9})

	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		rep, err := rec.RunOnce(ctx, "ac")
		if err != nil {
			t.Fatalf("RunOnce #%d: %v", i, err)
		}
		if i < 3 && rep.Alerts != 0 {
			t.Fatalf("第 %d 次不应告警（阈值为 %d）：%+v", i, mismatchAlertThreshold, rep)
		}
		if i == 3 && rep.Alerts != 1 {
			t.Fatalf("第 3 次连续不一致应告警 1 次：%+v", rep)
		}
	}
}

// TestApprovalReconcilerUnconfigured 未配置外部 check 端口：Configured=false（端点据此 503），不 panic。
func TestApprovalReconcilerUnconfigured(t *testing.T) {
	db := storetest.NewDB(t)
	rec := NewApprovalReconciler(db, nil, nil, nil, nil, nil)
	rep, err := rec.RunOnce(context.Background(), "ac")
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if rep.Configured {
		t.Fatalf("未配置 checker 时 Configured 应为 false：%+v", rep)
	}
}
