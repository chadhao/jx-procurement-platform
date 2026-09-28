package inbox

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// JobTypeFetchDetail 事件处理后需拉取实例详情的作业类型。
const JobTypeFetchDetail = "fetch_detail"

// JobTypeOrgSync 通讯录（部门/人员）变更事件的作业类型（docs/08 §4.6-b，批次二落地）。
//
// ★ 通讯录事件**没有 instance_code**，走 fetch_detail 必失败入死信 ⇒ 必须按
// event_type 在入队时即分流；消费端见 internal/worker 的 org_sync 分支
// （落库走 orgsync.EventHandler.HandleContactEvent，绝不经审批 ingest 链）。
const JobTypeOrgSync = "org_sync"

// IsOrgDirectoryEvent 判定事件类型是否属通讯录（部门/人员）变更事件族。
//
// ★ 官方已核对的 6 个键均带 `contact.` 前缀（docs/reference/README.md）；
// 本批订阅的就是这 6 个，前缀判定同时天然覆盖未来的 contact.* 新键
// （未订阅的键长连接侧收不到，收到的必然是已注册键）。
func IsOrgDirectoryEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "contact.")
}

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
		IdemKey:   ev.IdemKey,
		EventType: defaultEventType(ev.EventType),
		// 通讯录事件没有 instance_code（NOT NULL 列允许空串，docs/08 §4.6-b）。
		InstanceCode: ev.InstanceCode,
		Status:       ev.Status,
		EventID:      ev.IdemKey,
		Payload:      string(payload),
		ProcessState: "PENDING",
		ReceivedAt:   s.now(),
	}
	// ★ 分流（docs/08 §4.6-b）：contact.* → org_sync 作业；其余（审批事件等）→
	// fetch_detail，既有语义一字不改。判定在入队时做（而非消费时），
	// 保证 org_sync 作业永不被"缺少 instance_code"误判。
	jobType := JobTypeFetchDetail
	if IsOrgDirectoryEvent(ev.EventType) {
		jobType = JobTypeOrgSync
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
			created, err := s.db.EnqueueJobIgnore(ctx, tx, id, jobType, s.now())
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
