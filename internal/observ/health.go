package observ

import (
	"sync"
	"time"
)

// CheckSnapshot 启动自检四项快照（架构 §7.3 / FR-M0-05）。
type CheckSnapshot struct {
	Subscribe      bool `json:"subscribe"`       // 订阅状态：全部 approval_code 逐个订阅成功
	LongConn       bool `json:"longconn"`        // 长连接状态
	DBWritable     bool `json:"db_writable"`     // 数据库可写
	SingleInstance bool `json:"single_instance"` // 单实例（锁 + 端口）
}

// Health 聚合启动自检与运行期状态，供 /healthz 与 /readyz 使用。
type Health struct {
	mu             sync.RWMutex
	subscribe      bool
	longConn       bool
	dbWritable     bool
	singleInstance bool
	subscribeFail  []string // 订阅失败的 approval_code 列表
	startedAt      time.Time
	version        string
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

// Checks 返回四项自检快照。
func (h *Health) Checks() CheckSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return CheckSnapshot{
		Subscribe:      h.subscribe,
		LongConn:       h.longConn,
		DBWritable:     h.dbWritable,
		SingleInstance: h.singleInstance,
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
