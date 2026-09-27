package sync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// defaultLookback 首次对账默认回看时长（尚无游标时）。
const defaultLookback = 30 * 24 * time.Hour

// Reconciler 旧「实例对账补拉」器 —— ★ ③ 下**已作废（deprecated），勿恢复**。
//
// ★ R23（"对账"名义下的隐蔽覆盖）：本器原会经 `ingestor.Ingest` 把飞书侧实例详情
//
//	"缺则补"回库；而 ③ 下**我方才是状态源** → 该"补拉"会用飞书侧旧数据**覆盖我方已推进
//	的状态**。现已：① bootstrap 不再装配/调度本器；② Run 不再调用 ingest（补录路径退役）。
//	③ 的新对账器为独立的 `external_instances/check` 方向判断对账（另行排期），**不复用本器**。
type Reconciler struct {
	db       *store.DB
	client   feishu.Client
	maps     *config.Maps
	m        *observ.Metrics
	log      *slog.Logger
	now      func() time.Time
	lookback time.Duration
}

// NewReconciler 构造对账器。
//
// ★ 原 `ingestor *worker.Ingestor` 形参**已删除**（R23 退役旧写入者）：本器不再持有、
//
//	也不再触发 ingest（补录路径已退役）。
func NewReconciler(db *store.DB, client feishu.Client, maps *config.Maps, m *observ.Metrics, log *slog.Logger) *Reconciler {
	if m == nil {
		m = observ.NewMetrics()
	}
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Reconciler{db: db, client: client, maps: maps, m: m,
		log: observ.WithComponent(log, "reconcile"), now: func() time.Time { return time.Now().UTC() },
		lookback: defaultLookback}
}

// WithLookback 设置默认回看时长。
func (r *Reconciler) WithLookback(d time.Duration) *Reconciler {
	if d > 0 {
		r.lookback = d
	}
	return r
}

// Run 执行一次对账。approvalCode 为空表示全部；from/to 为零值时使用游标 / 默认窗口。
// 返回 (缺失条数, 补录条数)。
func (r *Reconciler) Run(ctx context.Context, approvalCode string, from, to time.Time) (int, int, error) {
	codes := r.codes(approvalCode)
	if len(codes) == 0 {
		return 0, 0, nil
	}
	now := r.now()
	if to.IsZero() {
		to = now
	}

	totalMissing, totalFilled := 0, 0
	for _, code := range codes {
		f := from
		if f.IsZero() {
			f, _ = r.windowFor(ctx, code, to, r.lookback)
		}

		remote, err := r.listAll(ctx, code, f, to)
		if err != nil {
			return totalMissing, totalFilled, fmt.Errorf("对账 %s 取实例 ID 失败: %w", code, err)
		}
		local, err := r.db.ExistingCodes(ctx, code)
		if err != nil {
			return totalMissing, totalFilled, err
		}

		var missing []string
		for _, id := range remote {
			if id != "" && !local[id] {
				missing = append(missing, id)
			}
		}

		// ★ R23 退役：旧对账是"缺则补"——对每个缺失实例 GetInstanceDetail + ingestor.Ingest
		//   （把飞书侧详情重新入库）。③ 下我方才是状态源，这条补录路径会**覆盖我方已推进
		//   状态**，故此处**只发现、不再入库**：仅记录告警与计数，等新方向判断对账器接管。
		if len(missing) > 0 {
			r.log.Warn("对账发现缺失实例，但补录路径已退役（R23）：仅记录，不再 ingest",
				"approval_code", code, "missing", len(missing))
		}

		const filled = 0 // 补录已退役，恒为 0
		if err := r.recordCursor(ctx, code, to, len(missing), filled); err != nil {
			r.log.Error("记录对账游标失败", "approval_code", code, "error", err.Error())
		}
		r.m.AddReconcile(int64(len(missing)), int64(filled))
		r.log.Info("对账完成（补录已退役）", "approval_code", code, "missing", len(missing), "filled", filled,
			"from", f.Format(time.RFC3339), "to", to.Format(time.RFC3339))

		totalMissing += len(missing)
		totalFilled += filled
	}
	return totalMissing, totalFilled, nil
}

// codes 返回本次对账的 approval_code 列表。
func (r *Reconciler) codes(one string) []string {
	if strings.TrimSpace(one) != "" {
		return []string{strings.TrimSpace(one)}
	}
	if r.maps != nil && r.maps.Approval != nil {
		return r.maps.Approval.Codes()
	}
	return nil
}

// listAll 分页拉取时间窗内全部实例 ID。
func (r *Reconciler) listAll(ctx context.Context, code string, from, to time.Time) ([]string, error) {
	var (
		ids   []string
		token string
	)
	for {
		res, err := r.client.ListInstanceIDs(ctx, feishu.ListInstanceIDsRequest{
			ApprovalCode: code,
			From:         from,
			To:           to,
			PageToken:    token,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, res.InstanceIDs...)
		if res.NextPageToken == "" {
			break
		}
		token = res.NextPageToken
	}
	return ids, nil
}
