package orgsync

import (
	"context"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// health.go —— /healthz 的 org_sync 观测段刷新（Runner 全量/对账 与 EventHandler
// 事件增量共用；批次二抽出，避免两处各写一份快照组装）。
//
// ★ 口径：**只做观测暴露，绝不参与就绪门禁**（docs/08 §4.5：外部依赖故障不得
// 放大成本地宕机）。零值时间格式化为空串（未发生过 ≠ 0001-01-01）。

// refreshHealth 从镜像状态 + 最近运行流水组装快照写入 Health。
// h 为 nil 时静默跳过（单测可不装配健康聚合器）。
func refreshHealth(h *observ.Health, db *store.DB) {
	if h == nil || db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	snap := observ.OrgSyncSnapshot{}
	if st, err := db.GetOrgSyncState(ctx); err == nil {
		snap.LastFullSuccessAt = formatTimeOrEmpty(st.LastFullSuccessAt)
		snap.LastFullError = st.LastFullError
		snap.DeptCount = st.LastFullDeptCount
		snap.UserCount = st.LastFullUserCount
		snap.LastEventAt = formatTimeOrEmpty(st.LastEventAt)
	}
	if run, err := db.GetLatestOrgSyncRun(ctx); err == nil && run != nil {
		snap.LastRunAt = formatTimeOrEmpty(run.RunAt)
		snap.LastRunTrigger = run.Trigger
		snap.LastRunResult = run.Result
		snap.LastRunError = run.Error
	}
	h.SetOrgSync(snap)
}

// formatTimeOrEmpty 零值时间 → 空串（「从未发生」不该显示成公元元年）。
func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
