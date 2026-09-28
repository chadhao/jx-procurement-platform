package observ

import (
	"sync/atomic"
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
}

// NewMetrics 构造指标集。
func NewMetrics() *Metrics { return &Metrics{} }

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
func (m *Metrics) IncFeishuAPICall() { atomic.AddInt64(&m.feishuAPICallsTotal, 1) }

// IncDeadletter 死信数 +1。
func (m *Metrics) IncDeadletter() { atomic.AddInt64(&m.deadletterTotal, 1) }

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
		DeadletterTotal:       atomic.LoadInt64(&m.deadletterTotal),
		WorkerQueueDepth:      atomic.LoadInt64(&m.workerQueueDepth),
		OrgSyncFailureTotal:   atomic.LoadInt64(&m.orgSyncFailureTotal),
		OrgEventAppliedTotal:  atomic.LoadInt64(&m.orgEventAppliedTotal),
		OrgEventGapTotal:      atomic.LoadInt64(&m.orgEventGapTotal),
		OrgEventUnknownTotal:  atomic.LoadInt64(&m.orgEventUnknownTotal),
	}
}

// MetricsSnapshot 指标快照。
type MetricsSnapshot struct {
	EventInboxTotal       int64 `json:"event_inbox_total"`
	IdempotentHitTotal    int64 `json:"idempotent_hit_total"`
	RejectedNoEventID     int64 `json:"rejected_no_event_id_total"`
	ReconcileMissingTotal int64 `json:"reconcile_missing_total"`
	ReconcileFilledTotal  int64 `json:"reconcile_filled_total"`
	FeishuAPICallsTotal   int64 `json:"feishu_api_calls_total"`
	DeadletterTotal       int64 `json:"deadletter_total"`
	WorkerQueueDepth      int64 `json:"worker_queue_depth"`
	OrgSyncFailureTotal   int64 `json:"org_sync_failure_total"`
	// 批次二（事件增量 + 对账）：org_event_* 为通讯录事件处理计数；
	// org_sync_failure_total 含全量/对账/事件处理失败（worker 侧）。
	OrgEventAppliedTotal int64 `json:"org_event_applied_total"`
	OrgEventGapTotal     int64 `json:"org_event_gap_total"`
	OrgEventUnknownTotal int64 `json:"org_event_unknown_total"`
}
