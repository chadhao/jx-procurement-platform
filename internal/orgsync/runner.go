package orgsync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// runner.go —— 全量同步编排：拉取（部门 → 各部门用户去重）→ ApplyFull → 可见性收口。
//
// ★ 触发方式（docs/08 §4.5 + 本批裁定）：
//   - 启动：bootstrap 异步 `go RunIfStale(ctx)`——**不阻塞启动、不进就绪门禁**；
//     距上次成功超阈值（JX_ORG_SYNC_STALE_HOURS，默认 24h）才拉，避免每次重启打一波。
//     启动期网络故障 ≠ 启动失败（异步 + 失败可见 + 手动端点兜底）。
//   - 手动：POST /internal/org/sync（X-Internal-Token）→ RunNow（无视阈值强制拉）。
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
func (r *Runner) RunFull(ctx context.Context, trigger string) (*Report, error) {
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
	if r.health == nil {
		return
	}
	snap := observ.OrgSyncSnapshot{}
	if st, err := r.db.GetOrgSyncState(context.Background()); err == nil {
		snap.LastFullSuccessAt = st.LastFullSuccessAt.Format(time.RFC3339)
		snap.LastFullError = st.LastFullError
		snap.DeptCount = st.LastFullDeptCount
		snap.UserCount = st.LastFullUserCount
	}
	r.health.SetOrgSync(snap)
}
