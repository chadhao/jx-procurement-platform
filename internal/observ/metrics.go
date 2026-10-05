package observ

import (
	"sync"
	"sync/atomic"
	"time"
)

// Metrics 进程内关键指标（架构 §8.2）。使用原子计数，可被 /readyz 聚合暴露。
type Metrics struct {
	eventInboxTotal       int64
	idempotentHitTotal    int64
	rejectedNoEventID     int64
	reconcileMissingTotal int64
	reconcileFilledTotal  int64
	feishuAPICallsTotal   int64
	deadletterTotal       int64
	workerQueueDepth      int64
	orgSyncFailureTotal   int64
	orgEventAppliedTotal  int64
	orgEventGapTotal      int64
	orgEventUnknownTotal  int64
	// N-060 F2/F8：飞书 API 按月归集（FR-M0-08）＋ 配额水位（FR-M0-19 70/90）。
	feishuMu              sync.Mutex
	feishuAPICallsMonthly map[string]int64 // "2006-01" → 当月调用量
	feishuQuotaLevel      int32            // 0 / 70 / 90（atomic）
	// N-060 F12（FR-M3-06）：异步事件作业耗时（从「仅日志」到可查询指标）。
	eventJobDurationCount int64 // atomic
	eventJobDurationTotal int64 // atomic（毫秒累计）
	eventJobDurationMax   int64 // atomic（毫秒；CAS 单调）
}

// NewMetrics 构造指标集。
func NewMetrics() *Metrics {
	return &Metrics{feishuAPICallsMonthly: map[string]int64{}}
}

// IncEventInbox 事件入库量 +1（按 event_type / approval_code 维度可在日志侧再聚合）。
func (m *Metrics) IncEventInbox() { atomic.AddInt64(&m.eventInboxTotal, 1) }

// IncIdempotentHit 幂等命中数 +1（超时重推发生率，TC-01）。
func (m *Metrics) IncIdempotentHit() { atomic.AddInt64(&m.idempotentHitTotal, 1) }

// IncRejectedNoEventID 因缺少事件级唯一 ID 被拒绝入库的告警计数（新增纪律）。
func (m *Metrics) IncRejectedNoEventID() { atomic.AddInt64(&m.rejectedNoEventID, 1) }

// AddReconcile 对账缺失 / 补录计数累加（TC-05）。
func (m *Metrics) AddReconcile(missing, filled int64) {
	atomic.AddInt64(&m.reconcileMissingTotal, missing)
	atomic.AddInt64(&m.reconcileFilledTotal, filled)
}

// IncFeishuAPICall 飞书调用量 +1（配额管理，FR-M0-08 / TC-25）。
// ★ N-060 F8：同时按**自然月**归集（key=YYYY-MM，FR-M0-08「按月归集」）；
// F2：月用量是配额水位（70/90）的分子（QuotaLevel）。
func (m *Metrics) IncFeishuAPICall() {
	atomic.AddInt64(&m.feishuAPICallsTotal, 1)
	if m.feishuAPICallsMonthly == nil {
		m.feishuMu.Lock()
		if m.feishuAPICallsMonthly == nil {
			m.feishuAPICallsMonthly = map[string]int64{}
		}
		m.feishuMu.Unlock()
	}
	key := time.Now().Format("2006-01")
	m.feishuMu.Lock()
	m.feishuAPICallsMonthly[key]++
	m.feishuMu.Unlock()
}

// FeishuCallsMonth 指定自然月（YYYY-MM）的调用量；未知月＝0。
func (m *Metrics) FeishuCallsMonth(month string) int64 {
	m.feishuMu.Lock()
	defer m.feishuMu.Unlock()
	return m.feishuAPICallsMonthly[month]
}

// FeishuCallsThisMonth 当月（YYYY-MM）调用量。
func (m *Metrics) FeishuCallsThisMonth() int64 {
	return m.FeishuCallsMonth(time.Now().Format("2006-01"))
}

// monthlyCopy 快照用浅拷贝（防外部改内部 map）。
func (m *Metrics) monthlyCopy() map[string]int64 {
	m.feishuMu.Lock()
	defer m.feishuMu.Unlock()
	out := make(map[string]int64, len(m.feishuAPICallsMonthly))
	for k, v := range m.feishuAPICallsMonthly {
		out[k] = v
	}
	return out
}

// QuotaLevel 配额水位档（纯函数，可测）：0（<70%）/ 70 / 90（≥90%）。
// quota<=0 视为未配置 ⇒ 恒 0（不误报；配置侧默认 10000 基线 —— 设计不依赖限时 100 万，01a §5.5 约束1）。
func QuotaLevel(monthCalls, quota int64) int {
	if quota <= 0 || monthCalls <= 0 {
		return 0
	}
	pct := monthCalls * 100 / quota
	switch {
	case pct >= 90:
		return 90
	case pct >= 70:
		return 70
	default:
		return 0
	}
}

// SetQuotaLevel / QuotaLevelNow 配额水位存取（atomic；reconcile 每轮评估写入、
// notify/other 读取 —— 90% ⇒ 降非关键（Bot）调用，01a §5.5 约束3）。
func (m *Metrics) SetQuotaLevel(level int) { atomic.StoreInt32(&m.feishuQuotaLevel, int32(level)) }
func (m *Metrics) QuotaLevelNow() int      { return int(atomic.LoadInt32(&m.feishuQuotaLevel)) }

// IncDeadletter 死信数 +1。
func (m *Metrics) IncDeadletter() { atomic.AddInt64(&m.deadletterTotal, 1) }

// AddEventJobDuration 记录一条异步事件作业耗时样本（N-060 F12 · FR-M3-06）。
// count/total 用 atomic 累加、max 用 CAS 单调上升；avg 由 Snapshot 现算。
func (m *Metrics) AddEventJobDuration(ms int64) {
	if ms < 0 {
		ms = 0
	}
	atomic.AddInt64(&m.eventJobDurationCount, 1)
	atomic.AddInt64(&m.eventJobDurationTotal, ms)
	for {
		cur := atomic.LoadInt64(&m.eventJobDurationMax)
		if ms <= cur || atomic.CompareAndSwapInt64(&m.eventJobDurationMax, cur, ms) {
			break
		}
	}
}

// SetWorkerQueueDepth 设置 worker 队列积压（瞬时值）。
func (m *Metrics) SetWorkerQueueDepth(n int64) { atomic.StoreInt64(&m.workerQueueDepth, n) }

// IncOrgSyncFailure 通讯录同步失败计数 +1（静默防护 N5：失败必须可见，docs/08 §4.11-B）。
func (m *Metrics) IncOrgSyncFailure() { atomic.AddInt64(&m.orgSyncFailureTotal, 1) }

// IncOrgEventApplied 通讯录事件成功应用（upsert/软删）计数 +1（批次二：事件增量）。
func (m *Metrics) IncOrgEventApplied() { atomic.AddInt64(&m.orgEventAppliedTotal, 1) }

// IncOrgEventGap 通讯录事件字段缺口计数 +1（字段权限未开 ⇒ 事件体/回源详情字段为空；
// 静默防护 C 族：缺口必须可见，不静默落空值）。
func (m *Metrics) IncOrgEventGap() { atomic.AddInt64(&m.orgEventGapTotal, 1) }

// IncOrgEventUnknown 未识别的通讯录事件类型计数 +1（可见、不静默丢弃）。
func (m *Metrics) IncOrgEventUnknown() { atomic.AddInt64(&m.orgEventUnknownTotal, 1) }

// Snapshot 返回当前指标只读快照。
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		EventInboxTotal:       atomic.LoadInt64(&m.eventInboxTotal),
		IdempotentHitTotal:    atomic.LoadInt64(&m.idempotentHitTotal),
		RejectedNoEventID:     atomic.LoadInt64(&m.rejectedNoEventID),
		ReconcileMissingTotal: atomic.LoadInt64(&m.reconcileMissingTotal),
		ReconcileFilledTotal:  atomic.LoadInt64(&m.reconcileFilledTotal),
		FeishuAPICallsTotal:   atomic.LoadInt64(&m.feishuAPICallsTotal),
		FeishuQuotaLevel:      m.QuotaLevelNow(),
		FeishuAPICallsMonthly: m.monthlyCopy(),
		DeadletterTotal:       atomic.LoadInt64(&m.deadletterTotal),
		WorkerQueueDepth:      atomic.LoadInt64(&m.workerQueueDepth),
		OrgSyncFailureTotal:   atomic.LoadInt64(&m.orgSyncFailureTotal),
		OrgEventAppliedTotal:  atomic.LoadInt64(&m.orgEventAppliedTotal),
		OrgEventGapTotal:      atomic.LoadInt64(&m.orgEventGapTotal),
		OrgEventUnknownTotal:  atomic.LoadInt64(&m.orgEventUnknownTotal),
		EventJobDurationCount: atomic.LoadInt64(&m.eventJobDurationCount),
		EventJobDurationTotal: atomic.LoadInt64(&m.eventJobDurationTotal),
		EventJobDurationMax:   atomic.LoadInt64(&m.eventJobDurationMax),
		EventJobDurationAvg: func() int64 {
			c := atomic.LoadInt64(&m.eventJobDurationCount)
			if c == 0 {
				return 0
			}
			return atomic.LoadInt64(&m.eventJobDurationTotal) / c
		}(),
	}
}

// MetricsSnapshot 指标快照。
type MetricsSnapshot struct {
	EventInboxTotal       int64            `json:"event_inbox_total"`
	IdempotentHitTotal    int64            `json:"idempotent_hit_total"`
	RejectedNoEventID     int64            `json:"rejected_no_event_id_total"`
	ReconcileMissingTotal int64            `json:"reconcile_missing_total"`
	ReconcileFilledTotal  int64            `json:"reconcile_filled_total"`
	FeishuAPICallsTotal   int64            `json:"feishu_api_calls_total"`
	FeishuAPICallsMonthly map[string]int64 `json:"feishu_api_calls_monthly,omitempty"` // N-060 F8 按月归集
	FeishuQuotaLevel      int              `json:"feishu_quota_level"`                 // N-060 F2：0/70/90
	DeadletterTotal       int64            `json:"deadletter_total"`
	WorkerQueueDepth      int64            `json:"worker_queue_depth"`
	OrgSyncFailureTotal   int64            `json:"org_sync_failure_total"`
	// 批次二（事件增量 + 对账）：org_event_* 为通讯录事件处理计数；
	// org_sync_failure_total 含全量/对账/事件处理失败（worker 侧）。
	OrgEventAppliedTotal int64 `json:"org_event_applied_total"`
	OrgEventGapTotal     int64 `json:"org_event_gap_total"`
	OrgEventUnknownTotal int64 `json:"org_event_unknown_total"`
	// N-060 F12：异步事件作业耗时（FR-M3-06）—— count/total/max 毫秒；avg 现算。
	EventJobDurationCount int64 `json:"event_job_duration_count"`
	EventJobDurationTotal int64 `json:"event_job_duration_total_ms"`
	EventJobDurationMax   int64 `json:"event_job_duration_max_ms"`
	EventJobDurationAvg   int64 `json:"event_job_duration_avg_ms"`
}
