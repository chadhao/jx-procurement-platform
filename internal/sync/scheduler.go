package fsync

import (
	"context"
	"log/slog"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
)

// ★ 退役状态（R23）：本文件的「定时对账器」**已无任何调用方** —— 唯一装配点
// `cmd/jxapproval/bootstrap.go` 已在 Batch B 摘除（旧对账"补拉"会覆盖我方已推进状态）。
//
// ★ 为什么保留而非删除：退役的**证据**是「不再装配 + `bootstrap_wiring_test.go` 守卫」，
// 删除文件反而看不出它曾被退役。
//
// ★ 与新对账器的关系：新对账器**不复用**本 `Scheduler`/`Reconciler` —— 它是独立的
// `sync/approval_reconcile.go`（对 `external_instances/check` 的 diff 做**方向判断**与重推，
// 依 `docs/11 §4.1` / `R24`，另行排期），由新的调度装配（T03）驱动。本 `Scheduler` 仅为
// 已作废的「旧实例补拉器」的定时外壳，届时连同旧 `Reconciler` 一并处置。
//
// Scheduler 每日定时对账（可配置间隔；test 环境支持可控时间窗，架构 §4.5）。
type Scheduler struct {
	rec      *Reconciler
	interval time.Duration
	log      *slog.Logger
}

// NewScheduler 构造对账调度器。
func NewScheduler(rec *Reconciler, interval time.Duration, log *slog.Logger) *Scheduler {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &Scheduler{rec: rec, interval: interval, log: observ.WithComponent(log, "scheduler")}
}

// Run 阻塞运行：先立即对账一次，随后按 interval 周期执行，直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	if _, _, err := s.RunOnce(ctx); err != nil {
		s.log.Error("对账执行出错", "error", err.Error())
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, _, err := s.RunOnce(ctx); err != nil {
				s.log.Error("对账执行出错", "error", err.Error())
			}
		}
	}
}

// RunOnce 立即执行一次全量对账（使用默认时间窗）。
func (s *Scheduler) RunOnce(ctx context.Context) (int, int, error) {
	return s.rec.Run(ctx, "", time.Time{}, time.Time{})
}
