package orgsync

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// runner.go —— 全量同步编排：拉取（部门 → 各部门用户去重）→ ApplyFull → 可见性收口。
//
// ★ 触发方式（docs/08 §4.5 + 本批裁定 + 批次二对账）：
//   - 启动：bootstrap 异步 `go RunIfStale(ctx)`——**不阻塞启动、不进就绪门禁**；
//     距上次成功超阈值（JX_ORG_SYNC_STALE_HOURS，默认 24h）才拉，避免每次重启打一波。
//     启动期网络故障 ≠ 启动失败（异步 + 失败可见 + 手动端点兜底）。
//   - 手动：POST /internal/org/sync（X-Internal-Token）→ RunNow（无视阈值强制拉）。
//   - 对账（批次二，docs/08 §4.7）：`RunReconcileLoop` 周期检查，距上次成功全量超
//     JX_ORG_RECONCILE_HOURS（默认 168h=每周）⇒ RunFull(trigger="reconcile") ——
//     全量本身即对账（ApplyFull：远端为准，软删我方多出的、补我方缺失的、复活错删的），
//     防事件丢投/漏处理导致的漂移。
//
// ★ 并发护栏：RunFull 内 `running` 原子位保证同一进程**串行**执行（启动/手动/对账
// 三个触发源可能撞车；撞车方得到明确错误，不静默排队也不并发打飞书）。
//
// ★ 失败可见（docs/08 §4.11-B/N5/N6）：拉取或落库任何一步失败 ⇒
//   t_org_sync_run(result=failed) + last_full_error + org_sync_failure_total + /healthz.org_sync
//   + log.Error —— 绝不静默半成功；last_full_success_at 只在成功时推进。

// Runner 全量同步运行器。
type Runner struct {
	fetch      Fetcher
	db         *store.DB
	log        *slog.Logger
	metrics    *observ.Metrics
	health     *observ.Health
	staleAfter time.Duration

	running atomic.Bool // RunFull 串行护栏（docs/08 批次二）
}

// NewRunner 构造；staleAfter ≤ 0 时取默认 24h。
func NewRunner(f Fetcher, db *store.DB, m *observ.Metrics, h *observ.Health, log *slog.Logger, staleAfter time.Duration) *Runner {
	if log == nil {
		log = slog.Default()
	}
	if m == nil {
		m = observ.NewMetrics()
	}
	if staleAfter <= 0 {
		staleAfter = 24 * time.Hour
	}
	return &Runner{fetch: f, db: db, log: log, metrics: m, health: h, staleAfter: staleAfter}
}

// RunIfStale 距上次成功同步超过阈值才执行全量（启动路径；无记录 ⇒ 必拉）。
func (r *Runner) RunIfStale(ctx context.Context) error {
	st, err := r.db.GetOrgSyncState(ctx)
	if err == nil && !st.LastFullSuccessAt.IsZero() &&
		time.Since(st.LastFullSuccessAt) < r.staleAfter {
		r.log.Info("通讯录镜像较新，跳过启动全量",
			"last_full_success_at", st.LastFullSuccessAt.Format(time.RFC3339),
			"stale_after", r.staleAfter.String())
		return nil
	}
	if err != nil && err != store.ErrNotFound {
		return fmt.Errorf("orgsync: 读取同步状态失败: %w", err)
	}
	_, err = r.RunFull(ctx, "startup")
	return err
}

// RunNow 无视阈值强制全量（手动端点路径）。
func (r *Runner) RunNow(ctx context.Context) (*Report, error) {
	return r.RunFull(ctx, "manual")
}

// RunFull 执行一次全量：拉部门 → 逐部门拉用户（去重）→ ApplyFull。
// 任何一步失败 ⇒ 落 failed 流水 + 状态 + 计数 + 健康 + 日志（全部可见）。
// ★ 串行护栏：上一轮未结束时再次触发 ⇒ 明确报错（不排队、不并发打飞书）。
func (r *Runner) RunFull(ctx context.Context, trigger string) (*Report, error) {
	if !r.running.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("orgsync: 上一次通讯录同步仍在进行中，拒绝并发触发（trigger=%s）", trigger)
	}
	defer r.running.Store(false)

	start := time.Now()
	depts, err := r.fetch.ListDepartments(ctx)
	if err != nil {
		return nil, r.fail(ctx, trigger, start, fmt.Errorf("拉取部门失败: %w", err))
	}
	// 根部门 + 全部子部门逐个拉用户（find_by_department 只返回直属用户——官方文档核对）。
	deptIDs := make([]string, 0, len(depts))
	seenDept := make(map[string]bool, len(depts))
	for _, g := range depts {
		if !seenDept[g.OpenDepartmentID] {
			seenDept[g.OpenDepartmentID] = true
			deptIDs = append(deptIDs, g.OpenDepartmentID)
		}
	}
	var users []*store.OrgUser
	seenUser := make(map[string]bool)
	for _, id := range deptIDs {
		us, err := r.fetch.ListUsersByDepartment(ctx, id)
		if err != nil {
			return nil, r.fail(ctx, trigger, start, err)
		}
		for _, u := range us {
			if u.OpenID == "" || seenUser[u.OpenID] {
				continue // 缺 open_id 项由 ApplyFull 的缺口计数兜底（这里按无 ID 丢弃）
			}
			seenUser[u.OpenID] = true
			users = append(users, u)
		}
	}
	rep, err := r.applier().ApplyFull(ctx, trigger, depts, users)
	if err != nil {
		return nil, r.fail(ctx, trigger, start, err)
	}
	r.observOK(rep, nil)
	r.log.Info("通讯录全量同步完成",
		"trigger", trigger, "dept_total", rep.DeptTotal, "user_total", rep.UserTotal,
		"dept_added", rep.DeptAdded, "dept_updated", rep.DeptUpdated, "dept_soft_deleted", rep.DeptSoftDeleted,
		"user_added", rep.UserAdded, "user_updated", rep.UserUpdated, "user_soft_deleted", rep.UserSoftDeleted,
		"duration_ms", rep.DurationMS)
	return rep, nil
}

// applier 惰性构造（Runner 与 Applier 共用 db）。
func (r *Runner) applier() *Applier {
	return NewApplier(r.db, r.log)
}

// fail 失败收口：failed 流水 + last_full_error（不推进成功时刻）+ 计数 + 健康 + 日志。
func (r *Runner) fail(ctx context.Context, trigger string, start time.Time, cause error) error {
	now := time.Now().UTC()
	run := &store.OrgSyncRun{
		RunAt: now, Trigger: trigger, Result: "failed",
		Error: cause.Error(), DurationMS: time.Since(start).Milliseconds(),
	}
	if _, err := r.db.InsertOrgSyncRun(ctx, run); err != nil {
		r.log.Error("通讯录同步：写失败流水也失败（原始错误一并保留）",
			"error", cause.Error(), "run_error", err.Error())
	}
	// 状态：保留原成功时刻（不推进，N6），只刷新尝试时刻与错误。
	st := &store.OrgSyncState{
		LastFullAttemptAt: now, LastFullSuccessAt: time.Time{}, LastFullError: cause.Error(), UpdatedAt: now,
	}
	if prev, err := r.db.GetOrgSyncState(ctx); err == nil {
		st.LastFullSuccessAt = prev.LastFullSuccessAt
		st.LastEventAt = prev.LastEventAt
	}
	if err := r.db.SaveOrgSyncState(ctx, st); err != nil {
		r.log.Error("通讯录同步：写失败状态也失败", "error", err.Error())
	}
	r.metrics.IncOrgSyncFailure()
	r.observOK(nil, cause)
	r.log.Error("通讯录全量同步失败（可用 POST /internal/org/sync 手动重试）",
		"trigger", trigger, "error", cause.Error())
	return cause
}

// observOK 把最近一次结果写进 /healthz 的 org_sync 段（非就绪门禁，docs/08 §4.5）。
func (r *Runner) observOK(rep *Report, cause error) {
	refreshHealth(r.health, r.db)
}

// RunReconcileLoop 通讯录定期对账循环（docs/08 §4.7，批次二落地）。
//
// ★ 兜底目的：事件丢投 / 长连接断线期间漏事件 / 事件处理死信 ⇒ 镜像漂移；
//
//	对账口径＝**以飞书为准**（ApplyFull：我方多出的软删、缺失的补齐、错删的复活）。
//
// ★ 调度：每 checkEvery 检查一次，距上次**成功**全量超 reconcileAfter
//
//	（JX_ORG_RECONCILE_HOURS，默认 168h＝每周）才拉；永不全量 ⇒ 也拉（自愈首次空镜像）。
//
// ★ 启动首拉由 RunIfStale 负责（阈值 JX_ORG_SYNC_STALE_HOURS 更短），本循环只兜长周期。
// ★ ctx 取消即退出；与审批修复循环同款 ticker 形态（无新增调度设施）。
func (r *Runner) RunReconcileLoop(ctx context.Context, reconcileAfter, checkEvery time.Duration) {
	if reconcileAfter <= 0 {
		reconcileAfter = 168 * time.Hour
	}
	if checkEvery <= 0 {
		checkEvery = time.Hour
	}
	r.log.Info("通讯录对账循环启动", "reconcile_after", reconcileAfter.String(),
		"check_every", checkEvery.String())
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			st, err := r.db.GetOrgSyncState(ctx)
			if err != nil && err != store.ErrNotFound {
				r.log.Error("通讯录对账循环：读取同步状态失败（下一轮重试）", "error", err.Error())
				continue
			}
			if err == nil && !st.LastFullSuccessAt.IsZero() &&
				time.Since(st.LastFullSuccessAt) < reconcileAfter {
				continue // 镜像足够新：对账让位给事件增量
			}
			if _, err := r.RunFull(ctx, "reconcile"); err != nil {
				// 失败已在 RunFull 内落流水/状态/计数/健康段；此处补进程级告警。
				r.log.Error("通讯录定期对账失败（下一轮重试）", "error", err.Error())
			}
		}
	}
}
