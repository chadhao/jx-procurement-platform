package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

const eventPayload = `{"schema":"2.0","header":{"event_id":"ev-worker-0001",
	"event_type":"approval.instance.status_changed_v4"},
	"event":{"instance_code":"INST-W","status":"PENDING"}}`

// TestWorkerRetryThenSuccess 详情拉取失败后指数退避重试，最终成功（不产生重复记录）。
func TestWorkerRetryThenSuccess(t *testing.T) {
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	ctx := context.Background()

	// 先注入事件（同步路径）。
	if _, err := inbox.NewService(db, metrics, nil).Handle(ctx, []byte(eventPayload)); err != nil {
		t.Fatalf("注入事件失败: %v", err)
	}

	calls := 0
	client := &feishu.FakeClient{
		DetailFn: func(_ context.Context, code string) (*feishu.InstanceDetail, error) {
			calls++
			if calls < 3 {
				return nil, errors.New("模拟详情接口暂时失败")
			}
			return &feishu.InstanceDetail{InstanceCode: code, StatusRaw: "APPROVED", OccurredAt: time.Now().UTC()}, nil
		},
	}
	wk := NewWorker(db, client, NewIngestor(db, &config.Maps{}, nil), metrics, nil).
		WithBackoff(func(int) time.Duration { return 0 }). // 测试立即重试
		WithMaxAttempts(5)

	if _, err := wk.ProcessDueOnce(ctx); err != nil {
		t.Fatalf("处理作业出错: %v", err)
	}
	if calls != 3 {
		t.Errorf("详情接口调用次数 = %d, 期望 3（前两次失败后重试）", calls)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_instance WHERE instance_code='INST-W'`); n != 1 {
		t.Errorf("业务记录数 = %d, 期望 1（重试不产生重复）", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE state='DONE'`); n != 1 {
		t.Errorf("DONE 作业数 = %d, 期望 1", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox WHERE process_state='DONE'`); n != 1 {
		t.Errorf("DONE 收件箱数 = %d, 期望 1", n)
	}
}

// TestWorkerDeadletterAndReplay 达上限转死信，且可人工重放（TC-30 / FR-M3-07）。
func TestWorkerDeadletterAndReplay(t *testing.T) {
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	ctx := context.Background()

	if _, err := inbox.NewService(db, metrics, nil).Handle(ctx, []byte(eventPayload)); err != nil {
		t.Fatalf("注入事件失败: %v", err)
	}

	client := &feishu.FakeClient{
		DetailFn: func(_ context.Context, _ string) (*feishu.InstanceDetail, error) {
			return nil, errors.New("模拟详情接口持续失败")
		},
	}
	wk := NewWorker(db, client, NewIngestor(db, &config.Maps{}, nil), metrics, nil).
		WithBackoff(func(int) time.Duration { return 0 }).
		WithMaxAttempts(3)

	if _, err := wk.ProcessDueOnce(ctx); err != nil {
		t.Fatalf("处理作业出错: %v", err)
	}

	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_deadletter`); n != 1 {
		t.Fatalf("死信数 = %d, 期望 1（不静默丢失）", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox WHERE process_state='DEAD'`); n != 1 {
		t.Errorf("DEAD 收件箱数 = %d, 期望 1", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE state='DEAD'`); n != 1 {
		t.Errorf("DEAD 作业数 = %d, 期望 1", n)
	}
	if metrics.Snapshot().DeadletterTotal != 1 {
		t.Errorf("死信指标 = %d, 期望 1", metrics.Snapshot().DeadletterTotal)
	}

	// 人工重放：作业重置为 QUEUED、收件箱回到 PENDING、replay_count 留痕。
	dls, err := db.ListDeadletters(ctx, 10)
	if err != nil || len(dls) != 1 {
		t.Fatalf("读取死信失败: %v, len=%d", err, len(dls))
	}
	count, err := wk.ReplayDeadletter(ctx, dls[0].ID)
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if count != 1 {
		t.Errorf("replay_count = %d, 期望 1", count)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE state='QUEUED'`); n != 1 {
		t.Errorf("重放后 QUEUED 作业数 = %d, 期望 1", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox WHERE process_state='PENDING'`); n != 1 {
		t.Errorf("重放后 PENDING 收件箱数 = %d, 期望 1", n)
	}
}
