package inbox

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// JobTypeFetchDetail 事件处理后需拉取实例详情的作业类型。
const JobTypeFetchDetail = "fetch_detail"

// syncPathWarnThreshold 同步路径耗时告警阈值（架构 §4.2 红线：3 秒窗口）。
const syncPathWarnThreshold = time.Second

// Service 事件收件箱服务。
type Service struct {
	db  *store.DB
	m   *observ.Metrics
	log *slog.Logger
	now func() time.Time
}

// NewService 构造收件箱服务。
func NewService(db *store.DB, m *observ.Metrics, log *slog.Logger) *Service {
	if m == nil {
		m = observ.NewMetrics()
	}
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Service{db: db, m: m, log: observ.WithComponent(log, "inbox"), now: func() time.Time { return time.Now().UTC() }}
}

// WithClock 注入时钟（供测试）。
func (s *Service) WithClock(fn func() time.Time) *Service {
	if fn != nil {
		s.now = fn
	}
	return s
}

// Result 事件处理结果（同步路径返回值）。
type Result struct {
	Event      *Event
	InboxID    int64
	Duplicate  bool // true = 幂等命中（重复到达）
	JobCreated bool
	DurationMS int64
}

// Handle 事件入口（同步极短路径）：解析幂等键 → 写收件箱 + 落待处理作业 → 立即返回。
// 绝不在此调用飞书接口，也不 sleep（FR-M3-01 / FR-M3-09 / TC-03）。
func (s *Service) Handle(ctx context.Context, payload []byte) (Result, error) {
	start := s.now()

	ev, err := Extract(payload)
	if err != nil {
		// ★ 拒绝入库并告警，不得退化为时间戳兜底。
		s.m.IncRejectedNoEventID()
		s.log.Error("事件缺少幂等键，拒绝入库并告警",
			"error", err.Error(), "payload_len", len(payload))
		return Result{}, err
	}

	row := &store.InboxRow{
		IdemKey:      ev.IdemKey,
		EventType:    defaultEventType(ev.EventType),
		InstanceCode: ev.InstanceCode,
		Status:       ev.Status,
		EventID:      ev.IdemKey,
		Payload:      string(payload),
		ProcessState: "PENDING",
		ReceivedAt:   s.now(),
	}

	var res Result
	txErr := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		id, inserted, err := s.db.InsertInboxIgnore(ctx, tx, row)
		if err != nil {
			return err
		}
		res.InboxID = id
		res.Duplicate = !inserted
		if inserted {
			created, err := s.db.EnqueueJobIgnore(ctx, tx, id, JobTypeFetchDetail, s.now())
			if err != nil {
				return err
			}
			res.JobCreated = created
		}
		return nil
	})
	if txErr != nil {
		return Result{}, txErr
	}

	res.Event = ev
	if res.Duplicate {
		s.m.IncIdempotentHit()
	} else {
		s.m.IncEventInbox()
	}
	res.DurationMS = s.now().Sub(start).Milliseconds()

	logger := s.log.With(
		"instance_code", ev.InstanceCode,
		"event_type", ev.EventType,
		"inbox_id", res.InboxID,
		"duration_ms", res.DurationMS,
	)
	switch {
	case res.Duplicate:
		logger.Info("幂等命中，重复事件不新增业务记录")
	case res.DurationMS > syncPathWarnThreshold.Milliseconds():
		// 同步路径耗时接近红线 → 告警，说明异步化可能被破坏。
		logger.Warn("同步路径耗时接近 3 秒红线")
	default:
		logger.Info("事件已入库并建待处理作业")
	}
	return res, nil
}

// HandleEvent 实现 feishu.EventSink（长连接回调签名：只返回错误）。
// 同步路径纪律：只写收件箱 + 建待处理作业 + 立即返回。
func (s *Service) HandleEvent(ctx context.Context, payload []byte) error {
	_, err := s.Handle(ctx, payload)
	return err
}

func defaultEventType(t string) string {
	if t == "" {
		return "unknown"
	}
	return t
}
