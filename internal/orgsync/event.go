package orgsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// event.go —— 通讯录事件增量处理器（docs/08 §4.6 设计点 3，批次二落地）。
//
// ★ 数据流（与审批事件链完全隔离）：
//
//	长连接(feishu.longconn) → inbox.Handle（幂等落盘 + 按 event_type 分流 org_sync 作业）
//	→ worker.processJob（org_sync 分支）→ EventHandler.HandleContactEvent → 镜像 UPSERT/软删。
//
// ★★ 幂等与乱序策略（任务硬要求，逐条给口径）：
//  1. 去重：inbox 层 `t_event_inbox.UNIQUE(idem_key)`，幂等键 = header.event_id
//     （事件级唯一 ID——绝不用"实体 id + 状态"，教训见 internal/inbox/idempotent.go 头注）。
//     重复投递在入队时即被 INSERT OR IGNORE 吞掉，处理逻辑天然只跑一次；
//     本处理器自身也是幂等 UPSERT（重复应用结果不变），双层防护。
//  2. 乱序：created/updated **总是回源** contact/v3 详情接口拿「当前真相」再落库
//     （事件体字段可能不全——字段权限未开时 name/department_ids 为空；且 updated
//     先于 created 到达时，事件体时序不可信）。因此**应用顺序无关紧要**：
//     无论 created/updated 谁先到，回源结果都是服务端当前态；
//     回源失败 ⇒ 退回事件体字段（可见 Warn + gap 计数），属性级漂移由定期对账自愈。
//     deleted 事件只做软删标记，与 upsert 的组合最终由对账兜底收敛。
//
// ★★ 部门删除口径（任务要求给明确处理并写文档）：**只软删该部门本身（is_deleted=1），
// 不级联**。其下人员 / 子部门的归属由后续 user 事件与定期对账修正——理由：级联软删
// 会在「部门挂靠关系暂时变化」（如子部门整体迁移）场景下造成大面积误删，而镜像的
// 职责只是「与飞书一致」，飞书侧真删了子部门，会有对应事件 + 对账兜底。
//
// ★ 失败可见：缺 ID / 写库失败 ⇒ 返回 error（worker 退避重试 → 死信，全链可见）；
// 字段缺口（回源失败、name/department_ids 为空）⇒ org_event_gap_total + Warn，
// 绝不静默落空值伪装成功（docs/08 §4.11-C 同源纪律）。

// DetailFetcher 单实体详情拉取端口（事件回源；生产＝FeishuFetcher，测试＝桩）。
type DetailFetcher interface {
	// GetDepartment 拉取单个部门当前详情（open_department_id 或 department_id）。
	GetDepartment(ctx context.Context, id string) (*store.OrgDepartment, error)
	// GetUser 拉取单个用户当前详情（open_id）。
	GetUser(ctx context.Context, openID string) (*store.OrgUser, error)
}

// 编译期断言：生产实现同时满足全量拉取与详情回源两个端口。
var _ DetailFetcher = (*FeishuFetcher)(nil)

// contactEnvelope 通讯录事件 2.0 信封（官方：schema/header/event 三段；
// updated 事件 event 内含 old_object——仅更新字段的原始值，本处理器不依赖它，
// 统一回源拿全量当前态）。
type contactEnvelope struct {
	Header struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		TenantKey string `json:"tenant_key"`
	} `json:"header"`
	Event struct {
		Object    json.RawMessage `json:"object"`
		OldObject json.RawMessage `json:"old_object"`
	} `json:"event"`
}

// EventHandler 通讯录事件增量处理器（只依赖 store 镜像方法 + DetailFetcher —— docs/08 §4.10 包边界）。
type EventHandler struct {
	db      *store.DB
	details DetailFetcher
	m       *observ.Metrics
	health  *observ.Health
	log     *slog.Logger
	now     func() time.Time
}

// NewEventHandler 构造；details 为 nil 时事件增量仍可处理删除/纯 ID 事件，
// 但 created/updated 的回源会失败 → 退回事件体字段（可见缺口）。
func NewEventHandler(db *store.DB, details DetailFetcher, m *observ.Metrics, h *observ.Health, log *slog.Logger) *EventHandler {
	if m == nil {
		m = observ.NewMetrics()
	}
	if log == nil {
		log = slog.Default()
	}
	return &EventHandler{db: db, details: details, m: m, health: h,
		log: observ.WithComponent(log, "orgsync.event"), now: func() time.Time { return time.Now().UTC() }}
}

// WithClock 注入时钟（测试）。
func (h *EventHandler) WithClock(fn func() time.Time) *EventHandler {
	if fn != nil {
		h.now = fn
	}
	return h
}

// HandleContactEvent 处理一条通讯录变更事件（worker org_sync 作业分支调用）。
//
// 返回 error ⇒ worker 退避重试 / 死信（可见）；返回 nil ⇒ 已应用（含幂等重复、
// 字段缺口降级、未知 contact.* 事件——这些都不重试，避免对永久性缺口打重试风暴）。
func (h *EventHandler) HandleContactEvent(ctx context.Context, payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("orgsync: 通讯录事件报文为空")
	}
	var env contactEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return fmt.Errorf("orgsync: 通讯录事件报文非法 JSON: %w", err)
	}
	et := strings.TrimSpace(env.Header.EventType)
	// 幂等键已由 inbox 层用 header.event_id 保证；此处再核验便于排障留痕。
	if strings.TrimSpace(env.Header.EventID) == "" {
		return fmt.Errorf("orgsync: 通讯录事件缺少 header.event_id（拒绝处理）")
	}
	switch et {
	case "contact.department.created_v3", "contact.department.updated_v3":
		return h.applyDepartmentUpsert(ctx, &env)
	case "contact.department.deleted_v3":
		return h.applyDepartmentDeleted(ctx, &env)
	case "contact.user.created_v3", "contact.user.updated_v3":
		return h.applyUserUpsert(ctx, &env)
	case "contact.user.deleted_v3":
		return h.applyUserDeleted(ctx, &env)
	default:
		// 未知 contact.* 键：报文已落 t_event_inbox（不丢），计数 + Warn 可见即可；
		// 返回 nil 避免对「收到但不认识」的键打重试风暴（与 retired no-op 同理）。
		h.m.IncOrgEventUnknown()
		h.log.Warn("收到未识别的通讯录事件类型（已落收件箱留痕，未应用）",
			"event_type", et, "event_id", env.Header.EventID)
		return nil
	}
}

// applyDepartmentUpsert department.created_v3 / updated_v3 ⇒ upsert 该部门。
func (h *EventHandler) applyDepartmentUpsert(ctx context.Context, env *contactEnvelope) error {
	obj, err := parseObject(env.Event.Object)
	if err != nil {
		return fmt.Errorf("orgsync: 解析部门事件体失败: %w", err)
	}
	// ID 判定：事件体官方字段为 department_id / open_department_id；防御式取值。
	id := firstNonEmptyStr(obj.openDepartmentID, obj.departmentID)
	if id == "" {
		return fmt.Errorf("orgsync: 部门事件体缺少 ID（event_id=%s）", env.Header.EventID)
	}
	// ★ 回源拿「当前真相」（乱序免疫；字段权限缺口由详情接口同口径暴露）。
	g := &store.OrgDepartment{
		OpenDepartmentID:       id,
		DepartmentID:           obj.departmentID,
		ParentOpenDepartmentID: obj.parentDepartmentID,
		Name:                   obj.name,
		RawJSON:                obj.raw,
		Source:                 "event",
	}
	if od := obj.openDepartmentID; od != "" {
		g.OpenDepartmentID = od
	}
	h.backfillDepartment(ctx, g)
	if err := h.upsertDepartment(ctx, g); err != nil {
		return err
	}
	h.log.Info("通讯录事件：部门已增量落库", "open_department_id", g.OpenDepartmentID,
		"name", g.Name, "event_id", env.Header.EventID)
	return nil
}

// backfillDepartment 事件体字段不全（或一律）时回源详情接口补全；失败 ⇒ 保留事件体
// 字段 + gap 可见（不返回 error：字段权限类缺口重试也不会好，对账兜底）。
func (h *EventHandler) backfillDepartment(ctx context.Context, g *store.OrgDepartment) {
	if h.details == nil {
		return
	}
	cur, err := h.details.GetDepartment(ctx, g.OpenDepartmentID)
	if err != nil {
		h.m.IncOrgEventGap()
		h.log.Warn("通讯录事件：回源部门详情失败，退回事件体字段（对账兜底）",
			"open_department_id", g.OpenDepartmentID, "error", err.Error())
		return
	}
	// 回源结果为准（ID/父级/名称全部取当前态；事件体仅在回源缺失时兜底）。
	if cur.OpenDepartmentID != "" {
		g.OpenDepartmentID = cur.OpenDepartmentID
	}
	if cur.DepartmentID != "" {
		g.DepartmentID = cur.DepartmentID
	}
	if cur.ParentOpenDepartmentID != "" {
		g.ParentOpenDepartmentID = cur.ParentOpenDepartmentID
	}
	if cur.Name != "" {
		g.Name = cur.Name
	}
	if strings.TrimSpace(cur.RawJSON) != "" && cur.RawJSON != "{}" {
		g.RawJSON = cur.RawJSON
	}
}

// upsertDepartment 落库（保留 first_seen_at / 复活软删行）+ 事件时刻推进 + 健康/指标刷新。
func (h *EventHandler) upsertDepartment(ctx context.Context, g *store.OrgDepartment) error {
	now := h.now()
	prev, err := h.db.GetOrgDepartment(ctx, g.OpenDepartmentID)
	switch {
	case err == nil && !prev.FirstSeenAt.IsZero():
		g.FirstSeenAt = prev.FirstSeenAt
	case err == nil:
		g.FirstSeenAt = now
	case err == store.ErrNotFound:
		g.FirstSeenAt = now
	default:
		return fmt.Errorf("orgsync: 读取部门镜像 %s 失败: %w", g.OpenDepartmentID, err)
	}
	g.IsDeleted = false // 远端有 ⇒ 复活/保持在用（docs/08 §4.8）
	g.LastSeenAt = now
	g.UpdatedAt = now
	if g.NamePath == "" {
		g.NamePath = h.deriveDeptNamePath(ctx, g)
	}
	if strings.TrimSpace(g.Name) == "" && g.OpenDepartmentID != rootDepartmentID {
		h.m.IncOrgEventGap()
		h.log.Warn("通讯录事件：部门名称为空（疑似字段权限未开通）", "open_department_id", g.OpenDepartmentID)
	}
	if err := h.db.UpsertOrgDepartment(ctx, g); err != nil {
		return fmt.Errorf("orgsync: 落部门 %s 失败: %w", g.OpenDepartmentID, err)
	}
	h.finish(ctx)
	return nil
}

// applyDepartmentDeleted department.deleted_v3 ⇒ 只软删该部门本身（口径见包/文件头注）。
func (h *EventHandler) applyDepartmentDeleted(ctx context.Context, env *contactEnvelope) error {
	obj, err := parseObject(env.Event.Object)
	if err != nil {
		return fmt.Errorf("orgsync: 解析部门删除事件体失败: %w", err)
	}
	id := firstNonEmptyStr(obj.openDepartmentID, obj.departmentID)
	if id == "" {
		return fmt.Errorf("orgsync: 部门删除事件体缺少 ID（event_id=%s）", env.Header.EventID)
	}
	deleted, err := h.db.SoftDeleteOrgDepartment(ctx, id, h.now())
	if err != nil {
		return fmt.Errorf("orgsync: 软删部门 %s 失败: %w", id, err)
	}
	h.finish(ctx)
	h.log.Info("通讯录事件：部门已软删（只删部门本身，人员/子部门由事件+对账修正）",
		"open_department_id", id, "newly_deleted", deleted, "event_id", env.Header.EventID)
	return nil
}

// applyUserUpsert user.created_v3 / updated_v3 ⇒ upsert 该用户（含 department_ids）。
func (h *EventHandler) applyUserUpsert(ctx context.Context, env *contactEnvelope) error {
	obj, err := parseObject(env.Event.Object)
	if err != nil {
		return fmt.Errorf("orgsync: 解析用户事件体失败: %w", err)
	}
	openID := firstNonEmptyStr(obj.openID, obj.userID)
	if openID == "" {
		return fmt.Errorf("orgsync: 用户事件体缺少 open_id/user_id（event_id=%s）", env.Header.EventID)
	}
	u := &store.OrgUser{
		OpenID:              obj.openID,
		UserID:              obj.userID,
		UnionID:             obj.unionID,
		Name:                obj.name,
		PrimaryDepartmentID: primaryDeptOf(obj.departmentIDs),
		DepartmentIDs:       obj.departmentIDs,
		IsResigned:          obj.status.isResigned,
		IsExited:            obj.status.isExited,
		IsFrozen:            obj.status.isFrozen,
		IsActivated:         obj.status.isActivated,
		IsUnjoin:            obj.status.isUnjoin,
		RawJSON:             obj.raw,
		Source:              "event",
	}
	if u.OpenID == "" {
		// 事件体只有 user_id：open_id 需要额外字段权限（contact:user.employee_id:readonly，
		// docs/reference/README.md V-3）且事件体官方以 open_id 为主 ⇒ 缺失即显式失败。
		return fmt.Errorf("orgsync: 用户事件体缺少 open_id（仅 user_id=%s，event_id=%s）", openID, env.Header.EventID)
	}
	h.backfillUser(ctx, u)
	// 离职/退出 ⇒ 软删（行保留）；在职 ⇒ 复活/保持在用（与全量同口径，docs/08 §4.8）。
	u.IsDeleted = u.IsResigned || u.IsExited
	if err := h.upsertUser(ctx, u); err != nil {
		return err
	}
	h.log.Info("通讯录事件：人员已增量落库", "open_id", u.OpenID, "name", u.Name,
		"event_id", env.Header.EventID)
	return nil
}

// backfillUser 同 backfillDepartment：回源当前态（含在职态——处理时刻晚于事件产生时刻，
// 服务端当前态即最新真相，与乱序免疫口径一致），失败退回事件体（gap 可见）。
func (h *EventHandler) backfillUser(ctx context.Context, u *store.OrgUser) {
	if h.details == nil {
		return
	}
	cur, err := h.details.GetUser(ctx, u.OpenID)
	if err != nil {
		h.m.IncOrgEventGap()
		h.log.Warn("通讯录事件：回源用户详情失败，退回事件体字段（对账兜底）",
			"open_id", u.OpenID, "error", err.Error())
		return
	}
	if cur.UnionID != "" {
		u.UnionID = cur.UnionID
	}
	if cur.UserID != "" {
		u.UserID = cur.UserID
	}
	if cur.Name != "" {
		u.Name = cur.Name
	}
	if len(cur.DepartmentIDs) > 0 {
		u.DepartmentIDs = cur.DepartmentIDs
		u.PrimaryDepartmentID = primaryDeptOf(cur.DepartmentIDs)
	}
	// 在职态同样以回源为准（与 created/updated 字段口径一致，见函数头注）。
	u.IsResigned, u.IsExited, u.IsFrozen = cur.IsResigned, cur.IsExited, cur.IsFrozen
	u.IsActivated, u.IsUnjoin = cur.IsActivated, cur.IsUnjoin
	if strings.TrimSpace(cur.RawJSON) != "" && cur.RawJSON != "{}" {
		u.RawJSON = cur.RawJSON
	}
}

// upsertUser 落库（保留 first_seen_at）+ 事件时刻推进 + 健康/指标刷新。
func (h *EventHandler) upsertUser(ctx context.Context, u *store.OrgUser) error {
	now := h.now()
	prev, err := h.db.GetOrgUser(ctx, u.OpenID)
	switch {
	case err == nil && !prev.FirstSeenAt.IsZero():
		u.FirstSeenAt = prev.FirstSeenAt
	case err == nil:
		u.FirstSeenAt = now
	case err == store.ErrNotFound:
		u.FirstSeenAt = now
	default:
		return fmt.Errorf("orgsync: 读取人员镜像 %s 失败: %w", u.OpenID, err)
	}
	u.LastSeenAt = now
	u.UpdatedAt = now
	u.EmployeeStatus = normalizeStatus(u.IsResigned, u.IsExited, u.IsFrozen, u.IsActivated, u.IsUnjoin)
	if strings.TrimSpace(u.Name) == "" {
		h.m.IncOrgEventGap()
		h.log.Warn("通讯录事件：人员姓名为空（疑似字段权限未开通）", "open_id", u.OpenID)
	}
	if len(u.DepartmentIDs) == 0 {
		h.m.IncOrgEventGap()
		h.log.Warn("通讯录事件：人员部门归属为空（疑似字段权限未开通）", "open_id", u.OpenID)
	}
	if err := h.db.UpsertOrgUser(ctx, u); err != nil {
		return fmt.Errorf("orgsync: 落人员 %s 失败: %w", u.OpenID, err)
	}
	h.finish(ctx)
	return nil
}

// applyUserDeleted user.deleted_v3 ⇒ 软删该用户（行保留，历史解析不受影响，docs/08 C-E）。
func (h *EventHandler) applyUserDeleted(ctx context.Context, env *contactEnvelope) error {
	obj, err := parseObject(env.Event.Object)
	if err != nil {
		return fmt.Errorf("orgsync: 解析用户删除事件体失败: %w", err)
	}
	openID := firstNonEmptyStr(obj.openID, obj.userID)
	if openID == "" {
		return fmt.Errorf("orgsync: 用户删除事件体缺少 open_id/user_id（event_id=%s）", env.Header.EventID)
	}
	deleted, err := h.db.SoftDeleteOrgUser(ctx, openID, h.now())
	if err != nil {
		return fmt.Errorf("orgsync: 软删人员 %s 失败: %w", openID, err)
	}
	h.finish(ctx)
	h.log.Info("通讯录事件：人员已软删（行保留，历史解析不受影响）",
		"open_id", openID, "newly_deleted", deleted, "event_id", env.Header.EventID)
	return nil
}

// finish 每次成功应用后的收口：last_event_at 推进 + applied 计数 + /healthz 快照刷新。
func (h *EventHandler) finish(ctx context.Context) {
	h.m.IncOrgEventApplied()
	if err := h.db.TouchOrgSyncEventAt(ctx, h.now()); err != nil {
		h.log.Error("通讯录事件：推进 last_event_at 失败（事件已应用，仅观测滞后）", "error", err.Error())
	}
	refreshHealth(h.health, h.db)
}

// deriveDeptNamePath 增量场景下的全路径派生：基于**本地镜像**逐级上溯（全量是按
// 远端集合派生；增量时远端集合未知，本地镜像 + 当前行足够，父链缺环时路径截断，
// 由对账全量重算修正——不静默伪造，也不因此失败）。
func (h *EventHandler) deriveDeptNamePath(ctx context.Context, g *store.OrgDepartment) string {
	rows, err := h.db.ListOrgDepartments(ctx)
	if err != nil {
		h.log.Warn("通讯录事件：读取镜像派生 name_path 失败（路径留空，对账修正）", "error", err.Error())
		return ""
	}
	nameBy := make(map[string]string, len(rows))
	parentBy := make(map[string]string, len(rows))
	for i := range rows {
		nameBy[rows[i].OpenDepartmentID] = rows[i].Name
		parentBy[rows[i].OpenDepartmentID] = rows[i].ParentOpenDepartmentID
	}
	nameBy[g.OpenDepartmentID] = g.Name
	parentBy[g.OpenDepartmentID] = g.ParentOpenDepartmentID
	var parts []string
	cur := g.OpenDepartmentID
	for i := 0; i < 32; i++ { // 深度上限：超限视为环（显式截断）
		if name, ok := nameBy[cur]; ok && strings.TrimSpace(name) != "" {
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

// ---------- 事件体解析（防御式：字段缺失不 panic，交由回源/缺口计数兜底） ----------

// objectStatus 用户事件体 status 子对象（解析留档；应用侧以回源为准，见 backfillUser）。
type objectStatus struct {
	isResigned, isExited, isFrozen, isActivated, isUnjoin bool
}

// eventObject 事件体 object 的并集字段（部门/用户取所需子集；多给的键忽略）。
type eventObject struct {
	openDepartmentID   string
	departmentID       string
	parentDepartmentID string
	name               string
	openID             string
	userID             string
	unionID            string
	departmentIDs      []string
	status             objectStatus
	raw                string
}

// parseObject 解析事件体 object（原始 JSON 保真进 raw；字段缺失一律容忍）。
func parseObject(raw json.RawMessage) (*eventObject, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("object 为空")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	o := &eventObject{openDepartmentID: rawStr(m, "open_department_id"),
		departmentID: rawStr(m, "department_id"), parentDepartmentID: rawStr(m, "parent_department_id"),
		name: rawStr(m, "name"), openID: rawStr(m, "open_id"),
		userID: rawStr(m, "user_id"), unionID: rawStr(m, "union_id"),
		raw: string(raw)}
	if ids, ok := m["department_ids"]; ok {
		var arr []string
		if err := json.Unmarshal(ids, &arr); err == nil {
			o.departmentIDs = arr
		}
	}
	if st, ok := m["status"]; ok {
		var sm map[string]bool
		if err := json.Unmarshal(st, &sm); err == nil {
			o.status = objectStatus{isResigned: sm["is_resigned"], isExited: sm["is_exited"],
				isFrozen: sm["is_frozen"], isActivated: sm["is_activated"], isUnjoin: sm["is_unjoin"]}
		}
	}
	return o, nil
}

// rawStr 读取 map 里的字符串值（非字符串/缺失 ⇒ 空串）。
func rawStr(m map[string]json.RawMessage, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// firstNonEmptyStr 取首个非空串。
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
