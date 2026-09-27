package sync

import (
	"context"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ★ 退役状态（R23）：本文件的游标读写（`windowFor` / `recordCursor`）仅被旧 `Reconciler.Run`
// 使用，而旧 `Reconciler` 已不再装配；`LastReconcile` **已无任何调用方**（勿据此以为 /readyz
// 会暴露它）。
//
// ★ 与新对账器的关系：`t_sync_cursor` 表在 ③ 下**要改语义复用** —— 对账对象由「实例」改为
// `external_instances/check`（依 `docs/11 §4.1` / `R24`）。故裁定为：**表保留、语义必改、
// 旧 `Reconciler` 代码不复用**；待新对账器（`sync/approval_reconcile.go`，另行排期）落地，
// 本文件的游标逻辑再按新语义处置（现保留，不删）。
//
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
