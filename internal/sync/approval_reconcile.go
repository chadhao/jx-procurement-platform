package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// approval_reconcile.go —— 新审批对账器（T03；04a §9.2 / docs/11 §4.1 / R06 / R24 / S14）。
//
// ★ 与旧 `Reconciler`（reconcile.go）**无关、不复用**：旧器是"缺则补"（经 ingest 落库，
//   `R23` 覆盖风险，已退役）；本器只对 `external_instances/check` 的 **diff 判方向**。
// ★ 独立于 `ingest`：本文件**不 import `internal/worker`**，不触碰旧事件链。
// ★ 唯一入口：`POST /internal/approval/check`（`R24`；旧 `/internal/sync/reconcile` 保持 410）。
//
// ★★ 方向判断纪律（本文件的核心）：
//   - 「我方领先或持平」（`local.UpdateTime >= remote.UpdateTime`）→ **安全重推**一次；
//   - 「飞书侧领先」（`local.UpdateTime <  remote.UpdateTime`）→ **禁止重推**（落后快照覆盖
//     会抹掉飞书侧新状态，`S14`）→ warn + 人工核对；
//   - 「方向不可判」（平台未回 `update_time`）→ warn + 人工核对，**不盲推**；
//   - 「平台有我方无」→ warn（人工核对，**不自动补**——补录路径已随 R23 退役）。
//
// 重推本身经 `*feishu.Pusher`：其内部**仅当 `update_time` 变大才推**、默认 `UPDATE`
// （第二道 S14 防线，`04a §3.1`）；本器再加一道方向门禁，二者互为双保险。

const (
	// defaultReconcileInterval 周期对账间隔（01a §6：每 5 分钟；★ 频率后续可自适应，本批固定）。
	defaultReconcileInterval = 5 * time.Minute
	// mismatchAlertThreshold 同一实例连续不一致达到该次数 → 告警（docs/02 §4.2）。
	mismatchAlertThreshold = 3
)

// ExtSyncChecker 远程差异检查端口（生产＝feishu.ExtCheckClient 的适配器）。
type ExtSyncChecker interface {
	CheckExternalInstances(ctx context.Context, approvalCode string) ([]RemoteInstanceState, error)
}

// RemoteInstanceState 平台侧观测到的实例状态（方向判断用）。
type RemoteInstanceState struct {
	InstanceID string
	UpdateTime int64
	Status     string
}

// ExtSyncCheckerFunc 把函数适配为 ExtSyncChecker（装配点便捷适配，避免额外适配器类型）。
type ExtSyncCheckerFunc func(ctx context.Context, approvalCode string) ([]RemoteInstanceState, error)

// CheckExternalInstances 实现 ExtSyncChecker。
func (f ExtSyncCheckerFunc) CheckExternalInstances(ctx context.Context, approvalCode string) ([]RemoteInstanceState, error) {
	return f(ctx, approvalCode)
}

// Repusher 重推端口（由 *feishu.Pusher 满足；只依赖一个方法，便于测试替身）。
type Repusher interface {
	Push(ctx context.Context, bizNo string) (feishu.PushResult, error)
}

// ReconcileReport 一次对账结果（供内部端点响应与观测）。
type ReconcileReport struct {
	// Configured 是否配置了外部 check 端口（false＝未装配，端点据此返回 503）。
	Configured bool
	// Checked 平台返回的差异实例条数（已扫描）。
	Checked int
	// Missing 平台有我方无（人工核对，不自动补）。
	Missing int
	// Repushed 我方领先/持平 → 已重推条数。
	Repushed int
	// StaleSkip 飞书侧领先 → 禁止覆盖而跳过条数（S14）。
	StaleSkip int
	// Undecidable 方向不可判（缺 update_time）→ 不盲推条数。
	Undecidable int
	// Alerts 触发连续不一致告警的实例数。
	Alerts int
}

// ApprovalReconciler 审批对账器（t_flow_task 在途实例 ↔ 飞书侧差异，判方向后重推）。
type ApprovalReconciler struct {
	db      *store.DB
	checker ExtSyncChecker
	pusher  Repusher
	maps    *config.Maps
	m       *observ.Metrics
	log     *slog.Logger
	now     func() time.Time

	mu     sync.Mutex
	streak map[string]int // instance_id → 连续不一致次数
}

// NewApprovalReconciler 构造审批对账器。checker/pusher 可为 nil（未配置 → 明确告警，不静默）。
func NewApprovalReconciler(db *store.DB, checker ExtSyncChecker, pusher Repusher,
	maps *config.Maps, m *observ.Metrics, log *slog.Logger) *ApprovalReconciler {
	if m == nil {
		m = observ.NewMetrics()
	}
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &ApprovalReconciler{
		db: db, checker: checker, pusher: pusher, maps: maps, m: m,
		log:    observ.WithComponent(log, "approval.reconcile"),
		now:    func() time.Time { return time.Now().UTC() },
		streak: map[string]int{},
	}
}

// Run 周期运行：先立即对账一次，随后按默认间隔执行，直到 ctx 取消。
//
// ★ 未配置外部 check 端口时**不启动周期任务**，并**明确告警**（可见，不静默跳过）——
// 使"对账没跑"能在启动日志里被发现，而不是体现为"一段时间后数据悄悄不对"。
func (r *ApprovalReconciler) Run(ctx context.Context) {
	if r.checker == nil {
		r.log.Warn("审批对账未配置外部 check 端口：周期对账不启动（★ 明确告警，非静默跳过）")
		return
	}
	if _, err := r.RunOnce(ctx, ""); err != nil && ctx.Err() == nil {
		r.log.Error("审批对账执行出错", "error", err.Error())
	}
	t := time.NewTicker(defaultReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := r.RunOnce(ctx, ""); err != nil && ctx.Err() == nil {
				r.log.Error("审批对账执行出错", "error", err.Error())
			}
		}
	}
}

// RunOnce 立即执行一次对账。approvalCode 为空＝对全部已配置 approval_code。
func (r *ApprovalReconciler) RunOnce(ctx context.Context, approvalCode string) (ReconcileReport, error) {
	rep := ReconcileReport{Configured: r.checker != nil}
	if r.checker == nil {
		return rep, nil
	}
	for _, code := range r.codes(approvalCode) {
		remote, err := r.checker.CheckExternalInstances(ctx, code)
		if err != nil {
			return rep, fmt.Errorf("对账 %s 取差异实例失败: %w", code, err)
		}
		for _, rs := range remote {
			rep.Checked++
			inst, err := r.lookupLocal(ctx, rs.InstanceID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					rep.Missing++
					r.log.Warn("对账：平台有我方无的实例（人工核对，不自动补）",
						"approval_code", code, "instance_id", rs.InstanceID)
					continue
				}
				return rep, err
			}

			// ★ 方向判断（本器核心）——
			switch {
			case rs.UpdateTime <= 0:
				// 不可判：平台未回 update_time → 不盲推。
				rep.Undecidable++
				r.log.Warn("对账：方向不可判（平台未回 update_time）→ 不盲推，人工核对",
					"biz_no", inst.BizNo, "instance_id", rs.InstanceID)
				r.bumpStreak(rs.InstanceID, &rep)
			case inst.UpdateTime < rs.UpdateTime:
				// 飞书侧领先：禁止落后快照覆盖（S14）。
				rep.StaleSkip++
				r.log.Warn("对账：飞书侧领先（我方 update_time 落后）→ 禁止落后快照覆盖（S14），不重推",
					"biz_no", inst.BizNo, "local_update_time", inst.UpdateTime, "remote_update_time", rs.UpdateTime)
				r.bumpStreak(rs.InstanceID, &rep)
			default:
				// 我方领先或持平 → 安全重推一次（UPDATE + update_time 单调）。
				if r.pusher == nil {
					r.log.Warn("对账：我方领先但未配置重推端口 → 跳过（可见告警）", "biz_no", inst.BizNo)
					continue
				}
				if _, err := r.pusher.Push(ctx, inst.BizNo); err != nil {
					r.log.Error("对账重推失败", "biz_no", inst.BizNo, "error", err.Error())
					continue
				}
				rep.Repushed++
				r.clearStreak(rs.InstanceID)
			}
		}
	}
	r.m.AddReconcile(int64(rep.Missing), int64(rep.Repushed))
	return rep, nil
}

// codes 返回本次对账的 approval_code 列表（空＝全部已配置）。
func (r *ApprovalReconciler) codes(one string) []string {
	if s := strings.TrimSpace(one); s != "" {
		return []string{s}
	}
	if r.maps != nil && r.maps.Approval != nil {
		return r.maps.Approval.Codes()
	}
	return nil
}

// lookupLocal 按平台 instance_id 定位我方实例。
//
// instance_id ＝我方 `InstanceCode`（＝ `{app_id}:{biz_no}`）；先按 instance_code 直取，
// 未命中则按 `:` 后缀当 `biz_no` 回退（兼容 appID 为空的历史数据）。均无 → ErrNotFound。
func (r *ApprovalReconciler) lookupLocal(ctx context.Context, instanceID string) (*store.Instance, error) {
	inst, err := r.db.GetInstance(ctx, instanceID)
	if err == nil {
		return inst, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if i := strings.LastIndex(instanceID, ":"); i >= 0 {
		if bizNo := strings.TrimSpace(instanceID[i+1:]); bizNo != "" {
			inst2, err2 := r.db.GetInstanceByBizNo(ctx, bizNo)
			if err2 == nil {
				return inst2, nil
			}
			if !errors.Is(err2, store.ErrNotFound) {
				return nil, err2
			}
		}
	}
	return nil, store.ErrNotFound
}

// bumpStreak 记录某实例"连续不一致"次数；达到阈值即告警（连续 3 次不一致 → 人工介入）。
func (r *ApprovalReconciler) bumpStreak(instanceID string, rep *ReconcileReport) {
	r.mu.Lock()
	r.streak[instanceID]++
	n := r.streak[instanceID]
	r.mu.Unlock()
	if n == mismatchAlertThreshold {
		rep.Alerts++
		r.log.Error("审批对账：同一实例连续多次不一致（≥阈值）→ 告警，需人工介入",
			"instance_id", instanceID, "streak", n)
	}
}

// clearStreak 某实例恢复一致（已安全重推）时清零其连续计数。
func (r *ApprovalReconciler) clearStreak(instanceID string) {
	r.mu.Lock()
	delete(r.streak, instanceID)
	r.mu.Unlock()
}
