package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	jsync "github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

const testInternalToken = "test-internal-token"

// newTestApp 装配一个开发模式路由（含 dev 注入端点）与依赖，供端到端验证。
func newTestApp(t *testing.T, status *string) (*echo.Echo, *store.DB, *worker.Worker, *observ.Metrics) {
	t.Helper()
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	maps := &config.Maps{}

	client := &feishu.FakeClient{
		DetailFn: func(_ context.Context, code string) (*feishu.InstanceDetail, error) {
			return &feishu.InstanceDetail{
				InstanceCode: code,
				ApprovalCode: "ac-todo-q1",
				StatusRaw:    *status,
				OccurredAt:   time.Now().UTC(),
			}, nil
		},
	}

	inboxSvc := inbox.NewService(db, metrics, nil)
	ingestor := worker.NewIngestor(db, maps, nil)
	wk := worker.NewWorker(db, client, ingestor, metrics, nil)
	sub := jsync.NewSubscriber(db, client, maps, metrics, nil)
	rec := jsync.NewReconciler(db, client, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub, Reconciler: rec,
		Perm: perm, Auth: auth, Maps: maps, WebUI: nil, Version: "test",
	})
	return e, db, wk, metrics
}

func inject(e *echo.Echo, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/dev/inject-event", strings.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Internal-Token", testInternalToken)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func event20(eventID, instance, status string) string {
	return `{"schema":"2.0","header":{"event_id":"` + eventID +
		`","event_type":"approval.instance.status_changed_v4"},"event":{"instance_code":"` + instance +
		`","status":"` + status + `","approval_code":"ac-todo-q1"}}`
}

// TestDevInjectIdempotent 同一 event_id 注入 3 次 → 收件箱 1 条、业务表 1 条（TC-01）。
func TestDevInjectIdempotent(t *testing.T) {
	status := "APPROVED"
	e, db, wk, metrics := newTestApp(t, &status)
	payload := event20("ev-idem-1", "INST-IT-1", "APPROVED")

	for i := 0; i < 3; i++ {
		rec := inject(e, payload)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次注入状态码 = %d, 期望 200, body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox`); n != 1 {
		t.Errorf("收件箱行数 = %d, 期望 1", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job`); n != 1 {
		t.Errorf("作业行数 = %d, 期望 1", n)
	}

	if _, err := wk.ProcessDueOnce(context.Background()); err != nil {
		t.Fatalf("处理作业失败: %v", err)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_instance WHERE instance_code='INST-IT-1'`); n != 1 {
		t.Errorf("业务表记录数 = %d, 期望 1", n)
	}
	if snap := metrics.Snapshot(); snap.IdempotentHitTotal != 2 {
		t.Errorf("幂等命中计数 = %d, 期望 2", snap.IdempotentHitTotal)
	}
}

// TestDevInjectMissingEventID 缺少事件 ID 的注入被拒绝且告警。
func TestDevInjectMissingEventID(t *testing.T) {
	status := "APPROVED"
	e, db, _, metrics := newTestApp(t, &status)

	rec := inject(e, `{"event":{"instance_code":"INST-IT-BAD","status":"APPROVED"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺事件 ID 应返回 400, 实际 %d", rec.Code)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox`); n != 0 {
		t.Errorf("被拒绝的事件不得入库，收件箱行数 = %d", n)
	}
	if metrics.Snapshot().RejectedNoEventID != 1 {
		t.Errorf("缺事件 ID 告警计数 = %d, 期望 1", metrics.Snapshot().RejectedNoEventID)
	}
}

// TestDevInjectRejectResubmit 驳回后重提：状态回到 PENDING 并最终收敛 APPROVED，历史链完整（TC-16）。
func TestDevInjectRejectResubmit(t *testing.T) {
	status := "PENDING"
	e, db, wk, _ := newTestApp(t, &status)
	ctx := context.Background()

	steps := []struct {
		eventID string
		status  string
	}{
		{"ev-rs-1", "PENDING"},
		{"ev-rs-2", "REJECTED"},
		{"ev-rs-3", "PENDING"}, // 驳回后重提：同一实例再次进入 PENDING（不得被幂等吞掉）
		{"ev-rs-4", "APPROVED"},
	}
	for _, s := range steps {
		status = s.status
		rec := inject(e, event20(s.eventID, "INST-IT-RS", s.status))
		if rec.Code != http.StatusOK {
			t.Fatalf("注入 %s 状态码 = %d, body=%s", s.eventID, rec.Code, rec.Body.String())
		}
		if _, err := wk.ProcessDueOnce(ctx); err != nil {
			t.Fatalf("处理作业失败: %v", err)
		}
	}

	// 业务表只有 1 条，最终状态收敛为 APPROVED。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_instance WHERE instance_code='INST-IT-RS'`); n != 1 {
		t.Fatalf("实例行数 = %d, 期望 1", n)
	}
	inst, err := db.GetInstance(ctx, "INST-IT-RS")
	if err != nil {
		t.Fatalf("读取实例失败: %v", err)
	}
	if inst.Status != "APPROVED" {
		t.Errorf("最终状态 = %q, 期望 APPROVED", inst.Status)
	}

	// 状态史保留完整链条：PENDING → REJECTED → PENDING → APPROVED。
	hist, err := db.ListHistory(ctx, "INST-IT-RS")
	if err != nil {
		t.Fatalf("读取状态史失败: %v", err)
	}
	want := []string{"PENDING", "REJECTED", "PENDING", "APPROVED"}
	if len(hist) != len(want) {
		t.Fatalf("状态史条数 = %d, 期望 %d", len(hist), len(want))
	}
	for i, h := range hist {
		if h.Status != want[i] {
			t.Errorf("状态史[%d] = %q, 期望 %q", i, h.Status, want[i])
		}
	}
}
