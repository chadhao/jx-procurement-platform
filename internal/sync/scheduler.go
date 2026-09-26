package sync

import (
	"context"
	"log/slog"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
)

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
