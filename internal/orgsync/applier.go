package orgsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// applier.go —— 全量落库（ApplyFull）：幂等 UPSERT + 软删只增不减 + 差异/缺口可见。
//
// ★ docs/08 §4.8 软删矩阵：远端有、本地有 → 复活（is_deleted=0）+ 更新属性；
//   远端有、本地无 → 新增；本地有、远端无 → 软删（绝不物理删除）；
//   离职（is_resigned/is_exited）→ 软删（列定义「离职/删除事件/全量缺失统一置 1」），
//   行保留（status 列照存），历史解析不受影响（C-E）。

// Report 一次全量的差异报告（落 t_org_sync_run + 日志 + /healthz 快照）。
type Report struct {
	Trigger         string `json:"trigger"`
	DeptAdded       int    `json:"dept_added"`
	DeptUpdated     int    `json:"dept_updated"`
	DeptSoftDeleted int    `json:"dept_soft_deleted"`
	UserAdded       int    `json:"user_added"`
	UserUpdated     int    `json:"user_updated"`
	UserSoftDeleted int    `json:"user_soft_deleted"`
	DeptTotal       int    `json:"dept_total"`
	UserTotal       int    `json:"user_total"`
	DurationMS      int64  `json:"duration_ms"`
}

// fieldGaps 字段覆盖缺口（docs/08 §4.11-C：字段权限没开导致字段为空 ⇒ 可见，不静默）。
type fieldGaps struct {
	DeptNameEmpty       int      `json:"dept_name_empty"`
	UserNameEmpty       int      `json:"user_name_empty"`
	UserDepartmentEmpty int      `json:"user_department_ids_empty"`
	UserOpenIDEmpty     int      `json:"user_open_id_empty"`
	Samples             []string `json:"samples,omitempty"` // 缺口样例（最多 5 条，供排障）
}

// Applier 全量落库器（只依赖 store 的镜像方法 —— docs/08 §4.10 包边界）。
type Applier struct {
	db  *store.DB
	log *slog.Logger
}

// NewApplier 构造。
func NewApplier(db *store.DB, log *slog.Logger) *Applier {
	if log == nil {
		log = slog.Default()
	}
	return &Applier{db: db, log: log}
}

// ApplyFull 把远端全量写入镜像并产出差异报告。
//
// ★ 防御（docs/08 §4.11-E）：远端部门或人员为空集 ⇒ 显式报错（疑似权限/接口异常），
// **不**推进 last_full_success_at（由 Runner 落 failed 流水）。
// ★ name_path 在内存中按远端集合派生（A/B/C），不依赖逐行回查。
func (a *Applier) ApplyFull(ctx context.Context, trigger string, depts []*store.OrgDepartment, users []*store.OrgUser) (*Report, error) {
	start := time.Now()
	if len(depts) == 0 || len(users) == 0 {
		return nil, fmt.Errorf("orgsync: 全量拉取为空（depts=%d users=%d），疑似权限/接口异常，拒绝落库", len(depts), len(users))
	}
	now := time.Now().UTC()

	// ---------- ① 差异基线：本地现存集合（含软删行，供「复活」判定与计数） ----------
	localDepts, err := a.db.ListOrgDepartments(ctx)
	if err != nil {
		return nil, fmt.Errorf("orgsync: 读取本地部门镜像失败: %w", err)
	}
	localUsers, err := a.db.ListOrgUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("orgsync: 读取本地人员镜像失败: %w", err)
	}
	existingDept := make(map[string]store.OrgDepartment, len(localDepts))
	for _, g := range localDepts {
		existingDept[g.OpenDepartmentID] = g
	}
	existingUser := make(map[string]store.OrgUser, len(localUsers))
	for _, u := range localUsers {
		existingUser[u.OpenID] = u
	}

	// ---------- ② name_path 派生（按远端集合本地计算） ----------
	nameBy := make(map[string]string, len(depts))
	parentBy := make(map[string]string, len(depts))
	for _, g := range depts {
		nameBy[g.OpenDepartmentID] = g.Name
		parentBy[g.OpenDepartmentID] = g.ParentOpenDepartmentID
	}
	pathOf := func(id string) string {
		var parts []string
		cur := id
		for i := 0; i < 32; i++ { // 深度上限：超限视为环（显式截断，不静默）
			name, ok := nameBy[cur]
			if !ok {
				break
			}
			if strings.TrimSpace(name) != "" {
				parts = append([]string{name}, parts...)
			}
			parent, ok := parentBy[cur]
			if !ok || parent == "" || parent == cur {
				break
			}
			cur = parent
		}
		return strings.Join(parts, "/")
	}

	// ---------- ③ 落部门 ----------
	rep := &Report{Trigger: trigger}
	seenDepts := make(map[string]bool, len(depts))
	gaps := &fieldGaps{}
	for _, g := range depts {
		if g.OpenDepartmentID == "" || seenDepts[g.OpenDepartmentID] {
			gaps.DeptNameEmpty++ // 异常项也必须可见（归入缺口计数，避免静默丢弃）
			gaps.Samples = append(gaps.Samples, "dept:"+g.OpenDepartmentID)
			continue
		}
		seenDepts[g.OpenDepartmentID] = true
		g.NamePath = pathOf(g.OpenDepartmentID)
		if strings.TrimSpace(g.Name) == "" && g.OpenDepartmentID != rootDepartmentID {
			gaps.DeptNameEmpty++
			a.appendSample(gaps, "dept:"+g.OpenDepartmentID)
		}
		g.IsDeleted = false // 远端有 ⇒ 复活/保持在用（§4.8）
		g.FirstSeenAt = firstSeen(existingDept, g.OpenDepartmentID, now)
		g.LastSeenAt = now
		g.UpdatedAt = now
		g.Source = trigger
		if err := a.db.UpsertOrgDepartment(ctx, g); err != nil {
			return nil, fmt.Errorf("orgsync: 落部门 %s 失败: %w", g.OpenDepartmentID, err)
		}
		if prev, ok := existingDept[g.OpenDepartmentID]; ok {
			if prev.Name != g.Name || prev.ParentOpenDepartmentID != g.ParentOpenDepartmentID ||
				prev.DepartmentID != g.DepartmentID || prev.IsDeleted {
				rep.DeptUpdated++
			}
		} else {
			rep.DeptAdded++
		}
	}

	// ---------- ④ 落人员（远端同一个人可能出现在多个部门 ⇒ 先去重） ----------
	seenUsers := make(map[string]bool, len(users))
	for _, u := range users {
		if strings.TrimSpace(u.OpenID) == "" {
			gaps.UserOpenIDEmpty++
			a.appendSample(gaps, "user:<empty open_id>")
			continue // 缺 open_id 无法落库——计缺口、不静默
		}
		if seenUsers[u.OpenID] {
			continue
		}
		seenUsers[u.OpenID] = true
		if strings.TrimSpace(u.Name) == "" {
			gaps.UserNameEmpty++
			a.appendSample(gaps, "user:"+u.OpenID)
		}
		if len(u.DepartmentIDs) == 0 {
			gaps.UserDepartmentEmpty++
			a.appendSample(gaps, "user:"+u.OpenID)
		}
		// 离职/退出 ⇒ 软删（行保留）；在职 ⇒ 复活/保持在用。
		u.IsDeleted = u.IsResigned || u.IsExited
		u.FirstSeenAt = firstSeenUser(existingUser, u.OpenID, now)
		u.LastSeenAt = now
		u.UpdatedAt = now
		u.Source = trigger
		if err := a.db.UpsertOrgUser(ctx, u); err != nil {
			return nil, fmt.Errorf("orgsync: 落人员 %s 失败: %w", u.OpenID, err)
		}
		if prev, ok := existingUser[u.OpenID]; ok {
			if prev.Name != u.Name || prev.PrimaryDepartmentID != u.PrimaryDepartmentID ||
				prev.IsResigned != u.IsResigned || prev.IsDeleted != u.IsDeleted {
				rep.UserUpdated++
			}
		} else {
			rep.UserAdded++
		}
	}

	// ---------- ⑤ 软删缺失（本地有、远端无；含已软删者不计新增软删数） ----------
	deletedDepts, err := a.db.SoftDeleteOrgDepartmentsExcept(ctx, seenDepts, now)
	if err != nil {
		return nil, fmt.Errorf("orgsync: 软删缺失部门失败: %w", err)
	}
	rep.DeptSoftDeleted = int(deletedDepts)
	deletedUsers, err := a.db.SoftDeleteOrgUsersExcept(ctx, seenUsers, now)
	if err != nil {
		return nil, fmt.Errorf("orgsync: 软删缺失人员失败: %w", err)
	}
	rep.UserSoftDeleted = int(deletedUsers)
	rep.DeptTotal = len(seenDepts)
	rep.UserTotal = len(seenUsers)
	rep.DurationMS = time.Since(start).Milliseconds()

	// ---------- ⑥ 差异流水 + 同步状态（成功才推进 last_full_success_at，N6） ----------
	gapsJSON, _ := json.Marshal(gaps)
	run := &store.OrgSyncRun{
		RunAt: now, Trigger: trigger, Result: "ok",
		DeptAdded: rep.DeptAdded, DeptUpdated: rep.DeptUpdated, DeptSoftDeleted: rep.DeptSoftDeleted,
		UserAdded: rep.UserAdded, UserUpdated: rep.UserUpdated, UserSoftDeleted: rep.UserSoftDeleted,
		FieldGapsJSON: string(gapsJSON), DurationMS: rep.DurationMS,
	}
	if _, err := a.db.InsertOrgSyncRun(ctx, run); err != nil {
		return nil, fmt.Errorf("orgsync: 写同步流水失败: %w", err)
	}
	st := &store.OrgSyncState{
		LastFullAttemptAt: now, LastFullSuccessAt: now, LastFullError: "",
		LastFullDeptCount: rep.DeptTotal, LastFullUserCount: rep.UserTotal, UpdatedAt: now,
	}
	if err := a.db.SaveOrgSyncState(ctx, st); err != nil {
		return nil, fmt.Errorf("orgsync: 写同步状态失败: %w", err)
	}
	if gaps.DeptNameEmpty+gaps.UserNameEmpty+gaps.UserDepartmentEmpty+gaps.UserOpenIDEmpty > 0 {
		// ★ 静默防护 C：字段缺口必须告警可见（docs/08 §4.11-C）。
		a.log.Warn("通讯录全量存在字段覆盖缺口（疑似字段权限未开通）",
			"dept_name_empty", gaps.DeptNameEmpty, "user_name_empty", gaps.UserNameEmpty,
			"user_department_ids_empty", gaps.UserDepartmentEmpty, "user_open_id_empty", gaps.UserOpenIDEmpty,
			"samples", fmt.Sprint(gaps.Samples))
	}
	return rep, nil
}

func (a *Applier) appendSample(gaps *fieldGaps, s string) {
	if len(gaps.Samples) < 5 {
		gaps.Samples = append(gaps.Samples, s)
	}
}

// firstSeen 保留原 first_seen_at（口径④：软删复活不清史）。
func firstSeen(existing map[string]store.OrgDepartment, id string, now time.Time) time.Time {
	if prev, ok := existing[id]; ok && !prev.FirstSeenAt.IsZero() {
		return prev.FirstSeenAt
	}
	return now
}

func firstSeenUser(existing map[string]store.OrgUser, id string, now time.Time) time.Time {
	if prev, ok := existing[id]; ok && !prev.FirstSeenAt.IsZero() {
		return prev.FirstSeenAt
	}
	return now
}
