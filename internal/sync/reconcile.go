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
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

// defaultLookback 首次对账默认回看时长（尚无游标时）。
const defaultLookback = 30 * 24 * time.Hour

// Reconciler 对账补拉：批量取实例 ID → 与本地集合求差 → 取详情幂等补录（架构 §4.5）。
// 这是唯一被允许的「非事件」数据入口；它不是轮询审批状态，而是按 ID 对账求差。
type Reconciler struct {
	db       *store.DB
	client   feishu.Client
	ingestor *worker.Ingestor
	maps     *config.Maps
	m        *observ.Metrics
	log      *slog.Logger
	now      func() time.Time
	lookback time.Duration
}

// NewReconciler 构造对账器。
func NewReconciler(db *store.DB, client feishu.Client, ingestor *worker.Ingestor, maps *config.Maps, m *observ.Metrics, log *slog.Logger) *Reconciler {
	if m == nil {
		m = observ.NewMetrics()
	}
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Reconciler{db: db, client: client, ingestor: ingestor, maps: maps, m: m,
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

		filled := 0
		for _, id := range missing {
			det, err := r.client.GetInstanceDetail(ctx, id)
			if err != nil {
				r.log.Error("补录取详情失败（次日窗口重叠补齐）", "instance_code", id, "error", err.Error())
				continue
			}
			if strings.TrimSpace(det.InstanceCode) == "" {
				det.InstanceCode = id
			}
			if err := r.ingestor.Ingest(ctx, det, worker.SourceReconcile); err != nil {
				r.log.Error("补录入库失败", "instance_code", id, "error", err.Error())
				continue
			}
			filled++
		}

		if err := r.recordCursor(ctx, code, to, len(missing), filled); err != nil {
			r.log.Error("记录对账游标失败", "approval_code", code, "error", err.Error())
		}
		r.m.AddReconcile(int64(len(missing)), int64(filled))
		r.log.Info("对账完成", "approval_code", code, "missing", len(missing), "filled", filled,
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
