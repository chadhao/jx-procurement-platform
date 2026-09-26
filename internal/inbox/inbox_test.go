package inbox

import (
	"context"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

const dupPayload = `{"schema":"2.0","header":{"event_id":"ev-dup-0001",
	"event_type":"approval.instance.status_changed_v4"},
	"event":{"instance_code":"INST-DUP","status":"APPROVED"}}`

// TestHandleDuplicateOnlyOneRow 事件重复到达只产生 1 条收件箱记录与 1 条作业（对应 TC-01）。
func TestHandleDuplicateOnlyOneRow(t *testing.T) {
	db := storetest.NewDB(t)
	m := observ.NewMetrics()
	svc := NewService(db, m, nil)
	ctx := context.Background()

	r1, err := svc.Handle(ctx, []byte(dupPayload))
	if err != nil {
		t.Fatalf("首次入库失败: %v", err)
	}
	if r1.Duplicate {
		t.Fatal("首次入库不应是幂等命中")
	}
	if !r1.JobCreated {
		t.Fatal("首次入库应创建待处理作业")
	}

	for i := 0; i < 2; i++ {
		r, err := svc.Handle(ctx, []byte(dupPayload))
		if err != nil {
			t.Fatalf("重复入库返回错误: %v", err)
		}
		if !r.Duplicate {
			t.Fatalf("第 %d 次重复到达应判定为幂等命中", i+2)
		}
		if r.InboxID != r1.InboxID {
			t.Errorf("幂等命中应指向同一收件箱行，got %d want %d", r.InboxID, r1.InboxID)
		}
	}

	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox`); n != 1 {
		t.Errorf("收件箱行数 = %d, 期望 1", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job`); n != 1 {
		t.Errorf("作业行数 = %d, 期望 1", n)
	}

	snap := m.Snapshot()
	if snap.EventInboxTotal != 1 {
		t.Errorf("事件入库计数 = %d, 期望 1", snap.EventInboxTotal)
	}
	if snap.IdempotentHitTotal != 2 {
		t.Errorf("幂等命中计数 = %d, 期望 2", snap.IdempotentHitTotal)
	}
}

// TestHandleMissingEventIDRejected 缺少事件 ID 的注入被拒绝且计入告警。
func TestHandleMissingEventIDRejected(t *testing.T) {
	db := storetest.NewDB(t)
	m := observ.NewMetrics()
	svc := NewService(db, m, nil)

	_, err := svc.Handle(context.Background(), []byte(`{"event":{"instance_code":"INST-X","status":"APPROVED"}}`))
	if err == nil {
		t.Fatal("缺少事件 ID 应被拒绝")
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox`); n != 0 {
		t.Errorf("被拒绝的事件不得入库，收件箱行数 = %d", n)
	}
	if m.Snapshot().RejectedNoEventID != 1 {
		t.Errorf("缺事件 ID 告警计数 = %d, 期望 1", m.Snapshot().RejectedNoEventID)
	}
}
