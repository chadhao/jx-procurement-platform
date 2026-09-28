package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// orgjob_test.go —— worker org_sync 作业分支测试（docs/08 §4.6-c 批次二）：
//   - org_sync 作业分派到 OrgEventHandler（不要求 instance_code、不经审批 ingest 链）；
//   - 失败可见：handler 报错 ⇒ 退避重试；达上限 ⇒ 死信（不静默丢事件）；
//   - 处理成功 ⇒ 作业/收件箱 DONE。

type recordingOrgHandler struct {
	calls    int
	payloads [][]byte
	err      error
}

func (h *recordingOrgHandler) HandleContactEvent(_ context.Context, payload []byte) error {
	h.calls++
	h.payloads = append(h.payloads, payload)
	return h.err
}

// newOrgTestEnv 构造 inbox+worker 共库的测试环境。
func newOrgTestEnv(t *testing.T, handler OrgEventHandler) (*inbox.Service, *Worker) {
	t.Helper()
	db := storetest.NewDB(t)
	svc := inbox.NewService(db, observ.NewMetrics(), observ.NewLogger("error", nil))
	w := NewWorker(db, nil, nil, observ.NewMetrics(), observ.NewLogger("error", nil)).
		WithOrgEventHandler(handler).
		WithBackoff(func(int) time.Duration { return 0 }) // 立即到期，测试内同步驱动
	return svc, w
}

// contactEvent2 构造通讯录事件报文。
func contactEvent2(eventID, eventType string) []byte {
	return []byte(`{"schema":"2.0","header":{"event_id":"` + eventID + `","event_type":"` + eventType + `"},` +
		`"event":{"object":{"open_id":"ou-1","name":"测试"}}}`)
}

// TestOrgSyncJobDispatchedToHandler org_sync 作业交给 handler，成功后 DONE。
func TestOrgSyncJobDispatchedToHandler(t *testing.T) {
	h := &recordingOrgHandler{}
	svc, w := newOrgTestEnv(t, h)
	ctx := context.Background()

	if _, err := svc.Handle(ctx, contactEvent2("ev-ok", "contact.user.created_v3")); err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	n, err := w.ProcessDueOnce(ctx)
	if err != nil {
		t.Fatalf("处理失败: %v", err)
	}
	if n != 1 || h.calls != 1 {
		t.Fatalf("org_sync 作业应被处理 1 次: processed=%d calls=%d", n, h.calls)
	}
	if len(h.payloads) != 1 || len(h.payloads[0]) == 0 {
		t.Fatalf("handler 应收到原始报文")
	}
	// 作业/收件箱 DONE；死信为 0。
	db := w.db
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE state='DONE'`); c != 1 {
		t.Fatalf("作业应 DONE: %d", c)
	}
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_deadletter`); c != 0 {
		t.Fatalf("成功路径不应有死信: %d", c)
	}
}

// TestOrgSyncJobFailureVisible handler 失败 ⇒ 退避重试，达上限 ⇒ 死信（失败可见）。
func TestOrgSyncJobFailureVisible(t *testing.T) {
	h := &recordingOrgHandler{err: errors.New("镜像写库失败")}
	svc, w := newOrgTestEnv(t, h)
	w.WithMaxAttempts(2)
	ctx := context.Background()

	if _, err := svc.Handle(ctx, contactEvent2("ev-fail", "contact.department.created_v3")); err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	// 第 1 轮：失败 → 重试；第 2 轮：失败 → 死信。
	for i := 0; i < 2; i++ {
		if _, err := w.ProcessDueOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮处理出错: %v", i+1, err)
		}
	}
	if h.calls != 2 {
		t.Fatalf("应重试共 2 次尝试: %d", h.calls)
	}
	db := w.db
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_deadletter`); c != 1 {
		t.Fatalf("达上限应落死信（不静默丢事件）: %d", c)
	}
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE state='DEAD'`); c != 1 {
		t.Fatalf("作业应 DEAD: %d", c)
	}
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox WHERE process_state='DEAD'`); c != 1 {
		t.Fatalf("收件箱应 DEAD: %d", c)
	}
}

// TestOrgSyncJobWithoutHandlerVisible 未装配 handler ⇒ 作业可见失败（不静默）。
func TestOrgSyncJobWithoutHandlerVisible(t *testing.T) {
	svc, w := newOrgTestEnv(t, nil)
	w.WithMaxAttempts(1)
	ctx := context.Background()
	if _, err := svc.Handle(ctx, contactEvent2("ev-none", "contact.user.deleted_v3")); err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	if _, err := w.ProcessDueOnce(ctx); err != nil {
		t.Fatalf("处理出错: %v", err)
	}
	if c := storetest.Count(t, w.db, `SELECT COUNT(*) FROM t_deadletter`); c != 1 {
		t.Fatalf("未装配 handler 应死信可见: %d", c)
	}
}
