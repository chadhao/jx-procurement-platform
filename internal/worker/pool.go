package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 作业与收件箱状态常量。
const (
	jobStateQueued  = "QUEUED"
	jobStateRunning = "RUNNING"
	jobStateDone    = "DONE"
	jobStateDead    = "DEAD"

	inboxPending    = "PENDING"
	inboxProcessing = "PROCESSING"
	inboxDone       = "DONE"
	inboxFailed     = "FAILED"
	inboxDead       = "DEAD"
)

const (
	defaultMaxAttempts  = 5
	defaultPollInterval = time.Second
	defaultBatchSize    = 20
)

// Worker 是异步处理池：取 PENDING 作业 → 拉详情 → 幂等落库 → 标记完成；
// 失败走指数退避重试，达上限转死信，支持人工重放（M3 / FR-M3-03 / FR-M3-07 / TC-30）。
type Worker struct {
	db       *store.DB
	client   feishu.Client
	ingestor *Ingestor
	m        *observ.Metrics
	log      *slog.Logger

	now          func() time.Time
	backoff      func(attempt int) time.Duration
	maxAttempts  int
	pollInterval time.Duration
	batchSize    int

	wg sync.WaitGroup
}

// NewWorker 构造处理池。
func NewWorker(db *store.DB, client feishu.Client, ingestor *Ingestor, m *observ.Metrics, log *slog.Logger) *Worker {
	if m == nil {
		m = observ.NewMetrics()
	}
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Worker{
		db:           db,
		client:       client,
		ingestor:     ingestor,
		m:            m,
		log:          observ.WithComponent(log, "worker"),
		now:          func() time.Time { return time.Now().UTC() },
		backoff:      Backoff,
		maxAttempts:  defaultMaxAttempts,
		pollInterval: defaultPollInterval,
		batchSize:    defaultBatchSize,
	}
}

// WithClock 注入时钟（测试）。
func (w *Worker) WithClock(fn func() time.Time) *Worker {
	if fn != nil {
		w.now = fn
	}
	return w
}

// WithBackoff 注入退避函数（测试可返回 0 以立即重试）。
func (w *Worker) WithBackoff(fn func(attempt int) time.Duration) *Worker {
	if fn != nil {
		w.backoff = fn
	}
	return w
}

// WithMaxAttempts 设置最大尝试次数（达上限转死信）。
func (w *Worker) WithMaxAttempts(n int) *Worker {
	if n > 0 {
		w.maxAttempts = n
	}
	return w
}

// WithBatchSize 设置单批处理条数。
func (w *Worker) WithBatchSize(n int) *Worker {
	if n > 0 {
		w.batchSize = n
	}
	return w
}

// Start 启动 n 个工作协程，直到 ctx 取消。启动即调度一次，随后按 pollInterval 轮询。
func (w *Worker) Start(ctx context.Context, n int) {
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		w.wg.Add(1)
		go w.loop(ctx)
	}
}

// Stop 等待工作协程退出（优雅退出：先停长连接，再排空 worker）。
func (w *Worker) Stop() { w.wg.Wait() }

func (w *Worker) loop(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.ProcessDueOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			w.log.Error("处理到期作业出错", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ProcessDueOnce 处理当前全部到期作业，返回处理条数（供测试同步驱动）。
func (w *Worker) ProcessDueOnce(ctx context.Context) (int, error) {
	total := 0
	for {
		jobs, err := w.db.DueJobs(ctx, w.now(), w.batchSize)
		if err != nil {
			return total, err
		}
		if len(jobs) == 0 {
			break
		}
		for i := range jobs {
			if err := w.processJob(ctx, jobs[i]); err != nil {
				w.log.Error("处理作业失败", "job_id", jobs[i].ID, "error", err)
			}
			total++
		}
		if depth, err := w.db.CountJobsByState(ctx, jobStateQueued); err == nil {
			w.m.SetWorkerQueueDepth(int64(depth))
		}
		// 不按批大小提前退出：退避为 0（或已在过去）的作业可能立即再次到期，
		// 需继续调度直至无到期作业；尝试次数有上限，故必然收敛（不会死循环）。
	}
	return total, nil
}

// processJob 处理单个作业：拉详情 → 幂等落库 → 标记完成；失败进入重试/死信。
func (w *Worker) processJob(ctx context.Context, job store.WorkerJob) error {
	// 置为处理中。
	if err := w.db.WithTx(ctx, func(tx *sql.Tx) error {
		if err := w.db.MarkJob(ctx, tx, job.ID, jobStateRunning, job.Attempts, nil, ""); err != nil {
			return err
		}
		return w.db.MarkInbox(ctx, tx, job.InboxID, inboxProcessing, job.Attempts, "", nil)
	}); err != nil {
		return err
	}

	ib, err := w.db.GetInbox(ctx, job.InboxID)
	if err != nil {
		return w.handleFailure(ctx, job, nil, fmt.Errorf("读取收件箱失败: %w", err))
	}

	instanceCode := strings.TrimSpace(ib.InstanceCode)
	if instanceCode == "" {
		if ev, exErr := inbox.Extract([]byte(ib.Payload)); exErr == nil {
			instanceCode = strings.TrimSpace(ev.InstanceCode)
		}
	}
	if instanceCode == "" {
		return w.handleFailure(ctx, job, ib, errors.New("事件缺少 instance_code，无法拉取详情"))
	}

	det, err := w.client.GetInstanceDetail(ctx, instanceCode)
	if err != nil {
		return w.handleFailure(ctx, job, ib, err)
	}
	if det.InstanceCode == "" {
		det.InstanceCode = instanceCode
	}
	if err := w.ingestor.Ingest(ctx, det, SourceEvent); err != nil {
		return w.handleFailure(ctx, job, ib, err)
	}

	now := w.now()
	return w.db.WithTx(ctx, func(tx *sql.Tx) error {
		if err := w.db.MarkJob(ctx, tx, job.ID, jobStateDone, job.Attempts+1, nil, ""); err != nil {
			return err
		}
		return w.db.MarkInbox(ctx, tx, job.InboxID, inboxDone, job.Attempts+1, "", &now)
	})
}

// handleFailure 失败处理：未达上限 → 指数退避重试；达上限 → 落死信并告警。
func (w *Worker) handleFailure(ctx context.Context, job store.WorkerJob, ib *store.InboxRow, cause error) error {
	attempts := job.Attempts + 1
	now := w.now()

	if attempts >= w.maxAttempts {
		payload := ""
		if ib != nil {
			payload = ib.Payload
		}
		err := w.db.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := w.db.InsertDeadletter(ctx, tx, job.InboxID, cause.Error(), payload); err != nil {
				return err
			}
			if err := w.db.MarkJob(ctx, tx, job.ID, jobStateDead, attempts, nil, cause.Error()); err != nil {
				return err
			}
			return w.db.MarkInbox(ctx, tx, job.InboxID, inboxDead, attempts, cause.Error(), &now)
		})
		if err == nil {
			w.m.IncDeadletter()
			w.log.Error("事件处理达上限转入死信（不静默丢失）",
				"job_id", job.ID, "inbox_id", job.InboxID, "attempts", attempts, "error", cause.Error())
		}
		return err
	}

	next := now.Add(w.backoff(attempts))
	err := w.db.WithTx(ctx, func(tx *sql.Tx) error {
		if err := w.db.MarkJob(ctx, tx, job.ID, jobStateQueued, attempts, &next, cause.Error()); err != nil {
			return err
		}
		return w.db.MarkInbox(ctx, tx, job.InboxID, inboxFailed, attempts, cause.Error(), nil)
	})
	if err == nil {
		w.log.Warn("作业处理失败，进入退避重试",
			"job_id", job.ID, "attempts", attempts, "next_run_at", next.Format(time.RFC3339), "error", cause.Error())
	}
	return err
}

// ReplayDeadletter 人工重放死信：复用原 payload 重投 worker，replay_count++ 留痕（TC-30）。
func (w *Worker) ReplayDeadletter(ctx context.Context, deadletterID int64) (int, error) {
	dl, err := w.db.GetDeadletter(ctx, deadletterID)
	if err != nil {
		return 0, err
	}
	now := w.now()
	err = w.db.WithTx(ctx, func(tx *sql.Tx) error {
		if err := w.db.ResetJobForReplay(ctx, tx, dl.InboxID); err != nil {
			return err
		}
		if err := w.db.MarkInbox(ctx, tx, dl.InboxID, inboxPending, 0, "", nil); err != nil {
			return err
		}
		return w.db.MarkReplayed(ctx, tx, deadletterID, now)
	})
	if err != nil {
		return 0, err
	}
	return dl.ReplayCount + 1, nil
}
