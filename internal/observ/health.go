package observ

import (
	"sync"
	"time"
)

// CheckSnapshot 启动自检快照（架构 §7.3 / FR-M0-05；含 docs/16 §2-C 定义装载位）。
type CheckSnapshot struct {
	Subscribe      bool `json:"subscribe"`       // 订阅状态：全部 approval_code 逐个订阅成功
	LongConn       bool `json:"longconn"`        // 长连接状态
	DBWritable     bool `json:"db_writable"`     // 数据库可写
	SingleInstance bool `json:"single_instance"` // 单实例（锁 + 端口）
	// ApprovalDefs 三方审批定义是否已装载（t_approval_def 非空；docs/16 §2-C 启动自检）。
	// ★ 只进快照、不进 Ready()：定义装载是**运维动作**（POST /api/admin/approval/defs/sync），
	//   把空表算作"未就绪"会让全新部署永远 503、连管理页都打不开去装载。
	ApprovalDefs bool `json:"approval_defs"`
}

// OrgSyncSnapshot 通讯录镜像同步状态（docs/08 §4.5：/healthz 的 org_sync 段，
// **非就绪门禁**——外部依赖故障不得放大成本地宕机）。
//
// ★ 批次二扩展：LastEventAt＝上次增量事件处理成功时刻（t_org_sync_state.last_event_at）；
// LastRun*＝最近一条 t_org_sync_run 流水（含对账 trigger=reconcile 的结果）。
type OrgSyncSnapshot struct {
	LastFullSuccessAt string `json:"last_full_success_at"`
	LastFullError     string `json:"last_full_error"`
	DeptCount         int    `json:"dept_count"`
	UserCount         int    `json:"user_count"`
	LastEventAt       string `json:"last_event_at"` // 增量事件最后处理时刻（零值＝尚未处理过事件）
	LastRunAt         string `json:"last_run_at"`   // 最近一次运行（全量/对账）时刻
	LastRunTrigger    string `json:"last_run_trigger"`
	LastRunResult     string `json:"last_run_result"` // ok / failed
	LastRunError      string `json:"last_run_error"`
}

// Health 聚合启动自检与运行期状态，供 /healthz 与 /readyz 使用。
type Health struct {
	mu             sync.RWMutex
	subscribe      bool
	longConn       bool
	dbWritable     bool
	singleInstance bool
	approvalDefs   bool
	subscribeFail  []string // 订阅失败的 approval_code 列表
	startedAt      time.Time
	version        string
	orgSync        OrgSyncSnapshot
}

// NewHealth 构造健康聚合器；version 为构建版本号。
func NewHealth(version string) *Health {
	return &Health{startedAt: time.Now().UTC(), version: version}
}

// SetSubscribe 设置订阅自检结果；failed 为失败的 approval_code 列表。
func (h *Health) SetSubscribe(ok bool, failed []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribe = ok
	h.subscribeFail = append([]string(nil), failed...)
}

// SetLongConn 设置长连接自检结果。
func (h *Health) SetLongConn(ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.longConn = ok
}

// SetDBWritable 设置数据库可写自检结果。
func (h *Health) SetDBWritable(ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dbWritable = ok
}

// SetSingleInstance 设置单实例自检结果。
func (h *Health) SetSingleInstance(ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.singleInstance = ok
}

// SetApprovalDefs 设置三方审批定义装载自检结果（t_approval_def 行数 > 0；docs/16 §2-C）。
func (h *Health) SetApprovalDefs(ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.approvalDefs = ok
}

// SetOrgSync 写入通讯录同步快照（观测位；**不参与 Ready()**，docs/08 §4.5）。
func (h *Health) SetOrgSync(s OrgSyncSnapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.orgSync = s
}

// OrgSync 读取通讯录同步快照。
func (h *Health) OrgSync() OrgSyncSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.orgSync
}

// Checks 返回自检快照（四项就绪位 + 定义装载位）。
func (h *Health) Checks() CheckSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return CheckSnapshot{
		Subscribe:      h.subscribe,
		LongConn:       h.longConn,
		DBWritable:     h.dbWritable,
		SingleInstance: h.singleInstance,
		ApprovalDefs:   h.approvalDefs,
	}
}

// SubscribeFailures 返回订阅失败的 approval_code 列表。
func (h *Health) SubscribeFailures() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.subscribeFail...)
}

// Ready 是否四项自检全部通过（就绪）。
func (h *Health) Ready() bool {
	c := h.Checks()
	return c.Subscribe && c.LongConn && c.DBWritable && c.SingleInstance
}

// StartedAt 返回进程启动时刻。
func (h *Health) StartedAt() time.Time { return h.startedAt }

// Version 返回构建版本号。
func (h *Health) Version() string { return h.version }
