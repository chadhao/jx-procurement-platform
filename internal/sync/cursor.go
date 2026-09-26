package sync

import (
	"context"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// cursorKindReconcile 对账游标类型。
const cursorKindReconcile = "reconcile"

// overlapBuffer 时间窗重叠缓冲：每次以 last_synced_at - 重叠为起点，避免边界漏单；
// 重叠范围内的重复由幂等吸收（架构 §4.5）。
const overlapBuffer = time.Hour

// windowFor 计算本次对账的时间窗 [from, to]。
// 若已有游标则以 (last_synced_at - overlapBuffer) 为起点，否则默认回看 lookback。
func (r *Reconciler) windowFor(ctx context.Context, approvalCode string, to time.Time, lookback time.Duration) (time.Time, time.Time) {
	from := to.Add(-lookback)
	if c, err := r.db.GetSyncCursor(ctx, approvalCode, cursorKindReconcile); err == nil && c.LastSyncedAt != nil {
		from = c.LastSyncedAt.Add(-overlapBuffer)
	}
	if from.After(to) {
		from = to
	}
	return from, to
}

// recordCursor 记录本次对账结果（缺失 / 补录计数，供次日窗口重叠补齐）。
func (r *Reconciler) recordCursor(ctx context.Context, approvalCode string, syncedAt time.Time, missing, filled int) error {
	now := r.now()
	return r.db.UpsertSyncCursor(ctx, store.SyncCursor{
		ApprovalCode: approvalCode,
		CursorKind:   cursorKindReconcile,
		LastSyncedAt: &syncedAt,
		LastRunAt:    &now,
		LastMissing:  missing,
		LastFilled:   filled,
	})
}

// LastReconcile 读取某 approval_code 上次对账摘要（供 /readyz 暴露）。
func (r *Reconciler) LastReconcile(ctx context.Context, approvalCode string) (missing, filled int, ok bool) {
	c, err := r.db.GetSyncCursor(ctx, approvalCode, cursorKindReconcile)
	if err != nil {
		return 0, 0, false
	}
	return c.LastMissing, c.LastFilled, true
}
