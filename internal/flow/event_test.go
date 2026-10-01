package flow_test

import (
	"context"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// recSub 记录收到的事件；对 INSTANCE_APPROVED 时回读库，验证"提交后分发"。
type recSub struct {
	db          *store.DB
	events      []flow.FlowEvent
	sawApproved bool
}

func (r *recSub) OnFlowEvent(ctx context.Context, ev flow.FlowEvent) {
	r.events = append(r.events, ev)
	if ev.Type == flow.EventInstanceApproved {
		if inst, err := r.db.GetInstanceByBizNo(ctx, ev.BizNo); err == nil && inst.Status == flow.InstanceApproved {
			r.sawApproved = true
		}
	}
}

// TestEventsDispatchedAfterCommit 事件在**事务提交后**统一分发（订阅者已能读到已提交状态）。
func TestEventsDispatchedAfterCommit(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	sub := &recSub{db: db}
	svc.Subscribe(sub)

	bizNo := submitOneNode(t, svc, "ou_m1")
	if len(sub.events) != 1 || sub.events[0].Type != flow.EventSubmitted {
		t.Fatalf("提交应分发 SUBMITTED，实际 %+v", sub.events)
	}
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil); err != nil {
		t.Fatal(err)
	}
	var sawTask, sawInst bool
	for _, ev := range sub.events {
		switch ev.Type {
		case flow.EventTaskApproved:
			sawTask = true
		case flow.EventInstanceApproved:
			sawInst = true
		}
	}
	if !sawTask || !sawInst {
		t.Errorf("应分发 TASK_APPROVED + INSTANCE_APPROVED，实际 %+v", sub.events)
	}
	if !sub.sawApproved {
		t.Errorf("INSTANCE_APPROVED 分发时订阅者应能读到已提交的 APPROVED（证明是提交后分发）")
	}
}

// panicSub 分发时 panic，验证订阅者异常被隔离、不影响主流程。
type panicSub struct{}

func (panicSub) OnFlowEvent(context.Context, flow.FlowEvent) { panic("订阅者故意 panic") }

// TestSubscriberPanicIsolated 订阅者 panic 不得影响主流程与其余订阅者。
func TestSubscriberPanicIsolated(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	rec := &recSub{db: db}
	svc.Subscribe(panicSub{})
	svc.Subscribe(rec)

	bizNo := submitOneNode(t, svc, "ou_m1") // 不应 panic 冒泡
	if len(rec.events) == 0 {
		t.Errorf("panic 订阅者不应阻止其余订阅者收到事件")
	}
	m1 := taskFor(t, db, bizNo, "ou_m1")
	if err := svc.Approve(ctx, bizNo, m1.TaskID, "ou_m1", "同意", nil); err != nil {
		t.Fatalf("订阅者 panic 不应影响审批主流程: %v", err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Errorf("实例 = %s, 期望 APPROVED", got)
	}
}
