package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// roleSysAdmin 唯一可进入「系统管理」的角色（PRD §4.1 / FR-M5-09）。
// 前端菜单隐藏不构成安全边界，服务端在此二次校验（架构 §5.6）。
const roleSysAdmin = "系统管理员"

// rowScopeOptions 行范围令牌 + 语义说明（6 令牌 + DENY，供前端下拉；Q3 定案不引入条件表达式）。
var rowScopeOptions = []map[string]string{
	{"token": "SELF", "label": "仅本人发起 / 本人经办"},
	{"token": "DEPT", "label": "本部门（含分管部门）"},
	{"token": "CHARGE_DEPT", "label": "所分管部门"},
	{"token": "ASSIGNED", "label": "本人被指定经办的记录"},
	{"token": "PARTICIPATED", "label": "本人参与验收的记录"},
	{"token": "ALL", "label": "全量"},
	{"token": "DENY", "label": "默认拒绝（无任何可见行）"},
}

// resourceOptions 资源枚举（供前端矩阵列）。
var resourceOptions = []map[string]string{
	{"key": "ledger:*", "label": "台账（全部类型，含 L01~L12）"},
	{"key": "dashboard:1", "label": "看板 1 · 预算执行"},
	{"key": "dashboard:2", "label": "看板 2 · 采购执行"},
	{"key": "dashboard:3", "label": "看板 3 · 费用结构"},
	{"key": "dashboard:4", "label": "看板 4 · 异常预警"},
	{"key": "api:instances", "label": "接口 · 实例查询"},
	{"key": "api:audit", "label": "接口 · 审计日志"},
}

// ---------- 角色守卫 ----------

// requireSysAdmin 解析会话并校验「系统管理员」；非管理员 → 40300 并留痕（API §3.9）。
// ★ N-075：判定改看 **Identity.SysRoles**（t_sys_role）——**不再看 Role**：
//
//	系统角色与审批角色解耦后，t_user_role 里不再有「系统管理员」；仅审批角色者
//	（如项目总经理）即使 Role 合法也必须 403。
func (d Deps) requireSysAdmin(c echo.Context) (permission.Identity, bool) {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		_ = fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
		return permission.Identity{}, false
	}
	if !hasSysRole(idn, roleSysAdmin) {
		d.audit(c.Request().Context(), &store.AuditLogRow{
			ActorOpenID: idn.OpenID, ActorRole: actorRoleOf(idn), Action: "denied",
			Resource: "admin", TargetID: c.Request().Method + " " + c.Request().URL.Path,
			Result: "deny", DetailJSON: `{"reason":"not_sys_admin"}`,
		})
		_ = fail(c, http.StatusForbidden, codeForbidden, "仅系统管理员可访问系统管理")
		return idn, false
	}
	return idn, true
}

// ---------- 权限矩阵 ----------

// handleAdminPermissionRulesGet 读取「角色 × 资源」权限矩阵 + 枚举。
func (d Deps) handleAdminPermissionRulesGet(c echo.Context) error {
	if _, ok := d.requireSysAdmin(c); !ok {
		return nil
	}
	rows, err := d.DB.ListPermissionRules(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, permissionRuleMap(r))
	}
	return ok(c, map[string]any{
		"items":      items,
		"resources":  resourceOptions,
		"roles":      permission.Roles,
		"row_scopes": rowScopeOptions,
	})
}

// adminRuleRequest PUT 请求体中的单条规则。
type adminRuleRequest struct {
	Resource       string   `json:"resource"`
	Role           string   `json:"role"`
	RowScope       string   `json:"row_scope"`
	ColumnAllow    []string `json:"column_allow"`
	ColumnDeny     []string `json:"column_deny"`
	WritableFields []string `json:"writable_fields"`
}

// handleAdminPermissionRulesPut 整表覆盖保存权限矩阵；保存后立即 Invalidate() → 下一请求生效（TC-33）。
func (d Deps) handleAdminPermissionRulesPut(c echo.Context) error {
	idn, isAdmin := d.requireSysAdmin(c)
	if !isAdmin {
		return nil
	}
	ctx := c.Request().Context()

	var req struct {
		Rules []adminRuleRequest `json:"rules"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if len(req.Rules) == 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "rules 不能为空")
	}

	// 校验 row_scope 白名单：只接受 6 令牌 + DENY，明确拒绝任何条件表达式字符串（Q3 定案）。
	newRules := make([]store.PermissionRule, 0, len(req.Rules))
	seen := map[string]bool{}
	for i, r := range req.Rules {
		resource := strings.TrimSpace(r.Resource)
		role := strings.TrimSpace(r.Role)
		scope := strings.ToUpper(strings.TrimSpace(r.RowScope))
		if resource == "" || role == "" {
			return fail(c, http.StatusBadRequest, codeBadRequest, "第 "+strconv.Itoa(i+1)+" 条规则缺少 resource/role")
		}
		if !validRowScope(scope) {
			return fail(c, http.StatusBadRequest, codeBadRequest,
				"row_scope 非法（仅允许 SELF/DEPT/CHARGE_DEPT/ALL/ASSIGNED/PARTICIPATED/DENY）: "+r.RowScope)
		}
		key := resource + "\x00" + role
		if seen[key] {
			return fail(c, http.StatusBadRequest, codeBadRequest, "重复的 resource × role: "+resource+" / "+role)
		}
		seen[key] = true
		newRules = append(newRules, store.PermissionRule{
			Resource:       resource,
			Role:           role,
			RowScope:       scope,
			ColumnAllow:    trimAll(r.ColumnAllow),
			ColumnDeny:     trimAll(r.ColumnDeny),
			WritableFields: trimAll(r.WritableFields),
		})
	}

	before, err := d.DB.ListPermissionRules(ctx)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	saved, err := d.DB.ReplacePermissionRules(ctx, newRules)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	// ★ 清缓存：规则变更不重启即生效（FR-M5-09 / TC-33）。
	d.Perm.Invalidate()

	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: actorRoleOf(idn), Action: "permission_update",
		Resource: "permission_rule", TargetID: "permission-rules", Result: "allow",
		DetailJSON: diffRules(before, newRules),
	})
	return ok(c, map[string]any{"saved": saved, "effective": "immediate"})
}

// ---------- 人员角色 ----------

// handleAdminUsersGet 列出「通讯录在册者」的人员角色视图。
//
// ★ 数据源＝**通讯录镜像 `t_org_user`（在册：is_deleted=0 ∧ is_resigned=0，全员覆盖）**，
// `role` / `extra_depts` / `active` / `updated_at` 来自 `t_user_role` **LEFT JOIN**（只读借用；
// 未绑角色者 role 为空串、无启用态与更新时间——不编造、不伪造）。
//
// ★ 动因（用户原话 2026-10-14）：「人员同步后，显示共 5 人，实际列表只有 4 人」——
// 旧实现以 `t_user_role` 为基（＝只列"已绑角色者"），通讯录新同步进来、尚未绑角色的人
// **不在列表里 ⇒ 无法给他分配角色** ⇒ 同步按钮承诺的"新增人员立刻可用"是假的。
// 以镜像为基后，列表条数＝通讯录在册人数（与同步报告口径一致），未绑角色者显示「未分配」。
//
// ★ 姓名 / 主部门一律取自镜像（飞书权威）——用户口径「不允许在后台修改」（见 PATCH 注释）。
func (d Deps) handleAdminUsersGet(c echo.Context) error {
	if _, ok := d.requireSysAdmin(c); !ok {
		return nil
	}
	rows, err := d.DB.ListAdminUsers(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, adminUserMap(r))
	}
	return ok(c, map[string]any{"items": items, "roles": permission.Roles})
}

// handleAdminUsersPost 给「通讯录在册者」新增角色映射。
//
// ★ 姓名 / 主部门**取自飞书镜像**（按 open_id 解析），**不接受后台传入**（用户口径
// 2026-10-14：「姓名和主部门需保持和飞书一致，不允许在我们的后台修改」）——
// 带了这两字段 ⇒ **显式 400 拒绝**（静默忽略＝"看不见的失败"）。
// ★ 值域：open_id **必须在通讯录镜像在册**（is_deleted=0 ∧ is_resigned=0）——否则
// 分配出来的是一个"系统里查无此人"的映射（与「点选镜像内用户」的口径一致）。
func (d Deps) handleAdminUsersPost(c echo.Context) error {
	idn, isAdmin := d.requireSysAdmin(c)
	if !isAdmin {
		return nil
	}
	ctx := c.Request().Context()

	var req struct {
		OpenID     string   `json:"open_id"`
		Name       string   `json:"name"`
		Role       string   `json:"role"`
		Department string   `json:"department"`
		ExtraDepts []string `json:"extra_depts"`
		Active     *bool    `json:"active"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	openID := strings.TrimSpace(req.OpenID)
	role := strings.TrimSpace(req.Role)
	if openID == "" || role == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "open_id 与 role 必填")
	}
	if !validRole(role) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "角色非法: "+role)
	}
	// ★ 飞书权威列：显式拒绝（不写入、不静默忽略）。
	if strings.TrimSpace(req.Name) != "" || strings.TrimSpace(req.Department) != "" {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"姓名 / 主部门来自飞书通讯录，本后台不可修改（请在飞书端修改后同步）")
	}
	if _, err := d.DB.GetUserRoleAny(ctx, openID); err == nil {
		return fail(c, http.StatusConflict, codeConflict, "该 open_id 已存在，请改用 PATCH 修改")
	}
	// ★ 姓名 / 主部门以镜像为唯一来源（不编造：不在册则拒绝）。
	name, department, inService, err := d.DB.GetOrgUserDisplay(ctx, openID)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "open_id 不在通讯录在册名单: "+openID+"（请先在飞书端添加并同步）")
	}
	if !inService {
		return fail(c, http.StatusNotFound, codeNotFound, "open_id 已离职/注销，不可分配角色: "+openID)
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}
	row := store.UserRole{
		OpenID: openID, Name: name, Role: role,
		Department: department, ExtraDepts: trimAll(req.ExtraDepts), Active: active,
	}
	if err := d.DB.UpsertUserRole(ctx, row); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: actorRoleOf(idn), Action: "permission_update",
		Resource: "user_role", TargetID: openID, Result: "allow",
		DetailJSON: diffUserRole(nil, row),
	})
	return ok(c, userRoleMap(row))
}

// handleAdminUsersPatch 修改人员角色映射（**角色 / 分管部门 / 启用停用**）。
//
// ★ 姓名 / 主部门**不接受**修改（用户原话 2026-10-14：「姓名和主部门需保持和飞书一致，
// 不允许在我们的后台修改（除非飞书端修改好了，同步过来）」）——二者是**飞书权威数据**，
// 以镜像为准。★ 带这两字段 ⇒ **显式 400 拒绝并说明原因**（静默忽略＝"看不见的失败"：
// 调用方以为改了、其实没改）。★ 至少要"不写入"——本实现直接拒绝，连写路径都不进。
//
// ★ 未绑角色者（镜像在册、但无 t_user_role 行）**可由此首次分配角色**：以镜像的
// 姓名 / 主部门播种新行——否则「同步进来的新人无法分配角色」，正是本页要修的问题。
// ★ 分管部门（extra_depts）是**我方业务配置**（非飞书数据）⇒ 仍可编辑。
func (d Deps) handleAdminUsersPatch(c echo.Context) error {
	idn, isAdmin := d.requireSysAdmin(c)
	if !isAdmin {
		return nil
	}
	ctx := c.Request().Context()
	openID := strings.TrimSpace(c.Param("open_id"))
	if openID == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "open_id 必填")
	}

	var req struct {
		Name       *string   `json:"name"`
		Role       *string   `json:"role"`
		Department *string   `json:"department"`
		ExtraDepts *[]string `json:"extra_depts"`
		Active     *bool     `json:"active"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	// ★ 飞书权威列：显式拒绝（不写入、不静默忽略）。
	if req.Name != nil || req.Department != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"姓名 / 主部门来自飞书通讯录，本后台不可修改（请在飞书端修改后同步）")
	}

	before, err := d.DB.GetUserRoleAny(ctx, openID)
	created := false
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		}
		// 未绑角色者：以镜像为基新建映射行（本页目的＝让在册者都能被分配角色）。
		name, dept, inService, oerr := d.DB.GetOrgUserDisplay(ctx, openID)
		if oerr != nil {
			return fail(c, http.StatusNotFound, codeNotFound, "open_id 不在通讯录在册名单: "+openID)
		}
		if !inService {
			return fail(c, http.StatusNotFound, codeNotFound, "open_id 已离职/注销，不可分配角色: "+openID)
		}
		before = &store.UserRole{OpenID: openID, Name: name, Department: dept}
		created = true
	}

	row := *before
	if req.Role != nil {
		r := strings.TrimSpace(*req.Role)
		if !validRole(r) {
			return fail(c, http.StatusBadRequest, codeBadRequest, "角色非法: "+r)
		}
		row.Role = r
	}
	if created && strings.TrimSpace(row.Role) == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "首次分配必须提供合法 role")
	}
	if req.ExtraDepts != nil {
		row.ExtraDepts = trimAll(*req.ExtraDepts)
	}
	if req.Active != nil {
		row.Active = *req.Active
	} else if created {
		row.Active = true // 首次分配默认启用（否则新人不生效＝本 bug 的形态）
	}

	if err := d.DB.UpsertUserRole(ctx, row); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	// 新建时 before 为 nil（无改前行），diff 只列 after。
	var beforeArg *store.UserRole
	if !created {
		beforeArg = before
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: actorRoleOf(idn), Action: "permission_update",
		Resource: "user_role", TargetID: openID, Result: "allow",
		DetailJSON: diffUserRole(beforeArg, row),
	})
	return ok(c, userRoleMap(row))
}

// ---------- 通讯录同步（管理端手动入口） ----------

// handleAdminOrgSync `POST /api/admin/org/sync` —— 系统管理员手动触发飞书通讯录全量同步。
//
// ★ 动因（用户原话 2026-10-09）：「人员管理」页需要「手动同步飞书人员和部门」的按钮。
//
//	全量同步能力早已具备，但既有入口 `POST /internal/org/sync`（handleOrgSync）挂在
//	internal 路由组、**需 `X-Internal-Token`** ⇒ 前端（带会话 cookie、无内部凭据）调不到。
//	故在管理端配置本入口，让「系统管理员」能在页面上自助触发。
//
// ★ 复用：同步逻辑**不重写** —— 直接调用 `d.OrgSync.RunNow(ctx)`（与内部端点同一运行器），
//
//	返回结构经 `orgSyncReportMap` 与 handleOrgSync **逐字段同构**。
//
// ★ 鉴权：走 `requireSysAdmin`（admin 域既有口径）；非系统管理员 ⇒ 40300 并留痕
//
//	（deny 记录在 requireSysAdmin 内落，不重复）。
//
// ★ 可见失败：`d.OrgSync == nil` ⇒ 503（不静默）；同步失败 ⇒ 502 + 明确错误。
// ★ 审计：Action=org_sync、Resource=org_directory、Result=allow；TargetID/DetailJSON
//
//	标注来源为「管理端」，以便与内部端点触发**区分入口**（同一动作、不同来源）。
func (d Deps) handleAdminOrgSync(c echo.Context) error {
	idn, isAdmin := d.requireSysAdmin(c)
	if !isAdmin {
		return nil // requireSysAdmin 已写出 40300 并留痕
	}
	if d.OrgSync == nil {
		return fail(c, http.StatusServiceUnavailable, codeInternal, "组织同步未装配（Runner 为 nil）")
	}
	ctx := c.Request().Context()
	rep, err := d.OrgSync.RunNow(ctx)
	if err != nil {
		return fail(c, http.StatusBadGateway, codeInternal, "通讯录全量同步失败: "+err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: actorRoleOf(idn),
		Action: "org_sync", Resource: "org_directory",
		TargetID: "admin/org/sync", Result: "allow",
		DetailJSON: `{"source":"admin"}`,
	})
	return ok(c, orgSyncReportMap(rep))
}

// ---------- 序列化与辅助 ----------

func permissionRuleMap(r store.PermissionRule) map[string]any {
	eff := ""
	if r.EffectiveFrom != nil {
		eff = r.EffectiveFrom.Format(time.RFC3339)
	}
	return map[string]any{
		"resource":        r.Resource,
		"role":            r.Role,
		"row_scope":       r.RowScope,
		"column_allow":    emptyIfNil(r.ColumnAllow),
		"column_deny":     emptyIfNil(r.ColumnDeny),
		"writable_fields": emptyIfNil(r.WritableFields),
		"effective_from":  eff,
		"remark":          r.Remark,
		"updated_at":      r.UpdatedAt.Format(time.RFC3339),
	}
}

func userRoleMap(r store.UserRole) map[string]any {
	return map[string]any{
		"open_id":     r.OpenID,
		"name":        r.Name,
		"role":        r.Role,
		"department":  r.Department,
		"extra_depts": emptyIfNil(r.ExtraDepts),
		"active":      r.Active,
		"updated_at":  r.UpdatedAt.Format(time.RFC3339),
	}
}

// adminUserMap 序列化「通讯录在册者」行视图（GET /api/admin/users 的 items[]）。
//
// ★ 未绑角色者：role=""（前端显示「未分配」）、extra_depts=[]、active=null、
//
//	updated_at=null —— **不编造**（无角色行就没有启用态与更新时间）。
//
// ★ 字段名保持既有（前端依赖）：open_id / name / role / department / extra_depts / active / updated_at。
func adminUserMap(r store.AdminUserRow) map[string]any {
	var active any
	if r.Active != nil {
		active = *r.Active
	}
	var updated any
	if r.UpdatedAt != nil {
		updated = r.UpdatedAt.Format(time.RFC3339)
	}
	return map[string]any{
		"open_id":     r.OpenID,
		"name":        r.Name,
		"role":        r.Role,
		"department":  r.Department,
		"extra_depts": emptyIfNil(r.ExtraDepts),
		"active":      active,
		"updated_at":  updated,
	}
}

// diffRules 生成「整表覆盖」的改前 / 改后 diff（仅列变更项与删除项，供 TC-35 审计）。
func diffRules(before, after []store.PermissionRule) string {
	beforeMap := map[string]store.PermissionRule{}
	for _, r := range before {
		beforeMap[r.Resource+"\x00"+r.Role] = r
	}
	afterKeys := map[string]bool{}
	changed := make([]map[string]any, 0)
	for _, a := range after {
		key := a.Resource + "\x00" + a.Role
		afterKeys[key] = true
		b, ok := beforeMap[key]
		if ok && ruleEquals(b, a) {
			continue
		}
		var beforeVal any
		if ok {
			beforeVal = ruleSnapshot(b)
		}
		changed = append(changed, map[string]any{
			"resource": a.Resource, "role": a.Role,
			"before": beforeVal, "after": ruleSnapshot(a),
		})
	}
	removed := make([]map[string]any, 0)
	for _, b := range before {
		if !afterKeys[b.Resource+"\x00"+b.Role] {
			removed = append(removed, map[string]any{"resource": b.Resource, "role": b.Role})
		}
	}
	out, _ := json.Marshal(map[string]any{
		"saved": len(after), "changed": changed, "removed": removed,
	})
	return string(out)
}

// diffUserRole 生成人员角色变更的改前 / 改后 diff。
func diffUserRole(before *store.UserRole, after store.UserRole) string {
	var b any
	if before != nil {
		b = map[string]any{
			"name": before.Name, "role": before.Role, "department": before.Department,
			"extra_depts": emptyIfNil(before.ExtraDepts), "active": before.Active,
		}
	}
	out, _ := json.Marshal(map[string]any{
		"before": b,
		"after": map[string]any{
			"name": after.Name, "role": after.Role, "department": after.Department,
			"extra_depts": emptyIfNil(after.ExtraDepts), "active": after.Active,
		},
	})
	return string(out)
}

func ruleSnapshot(r store.PermissionRule) map[string]any {
	return map[string]any{
		"row_scope":       r.RowScope,
		"column_allow":    emptyIfNil(r.ColumnAllow),
		"column_deny":     emptyIfNil(r.ColumnDeny),
		"writable_fields": emptyIfNil(r.WritableFields),
	}
}

func ruleEquals(a, b store.PermissionRule) bool {
	return strings.EqualFold(a.RowScope, b.RowScope) &&
		equalStrings(a.ColumnAllow, b.ColumnAllow) &&
		equalStrings(a.ColumnDeny, b.ColumnDeny) &&
		equalStrings(a.WritableFields, b.WritableFields)
}

func validRowScope(scope string) bool {
	for _, o := range rowScopeOptions {
		if o["token"] == scope {
			return true
		}
	}
	return false
}

func validRole(role string) bool {
	for _, r := range permission.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func trimAll(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func emptyIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
