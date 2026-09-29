package httpapi

import (
	"encoding/json"
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
func (d Deps) requireSysAdmin(c echo.Context) (permission.Identity, bool) {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		_ = fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
		return permission.Identity{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(idn.Role), roleSysAdmin) {
		d.audit(c.Request().Context(), &store.AuditLogRow{
			ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "denied",
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
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "permission_update",
		Resource: "permission_rule", TargetID: "permission-rules", Result: "allow",
		DetailJSON: diffRules(before, newRules),
	})
	return ok(c, map[string]any{"saved": saved, "effective": "immediate"})
}

// ---------- 人员角色 ----------

// handleAdminUsersGet 列出全部人员角色映射（含已停用）。
func (d Deps) handleAdminUsersGet(c echo.Context) error {
	if _, ok := d.requireSysAdmin(c); !ok {
		return nil
	}
	rows, err := d.DB.ListUserRoles(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, userRoleMap(r))
	}
	return ok(c, map[string]any{"items": items, "roles": permission.Roles})
}

// handleAdminUsersPost 新增人员角色映射。
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
	if _, err := d.DB.GetUserRoleAny(ctx, openID); err == nil {
		return fail(c, http.StatusConflict, codeConflict, "该 open_id 已存在，请改用 PATCH 修改")
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}
	row := store.UserRole{
		OpenID: openID, Name: strings.TrimSpace(req.Name), Role: role,
		Department: strings.TrimSpace(req.Department), ExtraDepts: trimAll(req.ExtraDepts), Active: active,
	}
	if err := d.DB.UpsertUserRole(ctx, row); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "permission_update",
		Resource: "user_role", TargetID: openID, Result: "allow",
		DetailJSON: diffUserRole(nil, row),
	})
	return ok(c, userRoleMap(row))
}

// handleAdminUsersPatch 修改人员角色映射（角色 / 部门 / 分管部门 / 启用停用）。
func (d Deps) handleAdminUsersPatch(c echo.Context) error {
	idn, isAdmin := d.requireSysAdmin(c)
	if !isAdmin {
		return nil
	}
	ctx := c.Request().Context()
	openID := strings.TrimSpace(c.Param("open_id"))

	before, err := d.DB.GetUserRoleAny(ctx, openID)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "open_id 不存在: "+openID)
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

	row := *before
	if req.Name != nil {
		row.Name = strings.TrimSpace(*req.Name)
	}
	if req.Role != nil {
		r := strings.TrimSpace(*req.Role)
		if !validRole(r) {
			return fail(c, http.StatusBadRequest, codeBadRequest, "角色非法: "+r)
		}
		row.Role = r
	}
	if req.Department != nil {
		row.Department = strings.TrimSpace(*req.Department)
	}
	if req.ExtraDepts != nil {
		row.ExtraDepts = trimAll(*req.ExtraDepts)
	}
	if req.Active != nil {
		row.Active = *req.Active
	}

	if err := d.DB.UpsertUserRole(ctx, row); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "permission_update",
		Resource: "user_role", TargetID: openID, Result: "allow",
		DetailJSON: diffUserRole(before, row),
	})
	return ok(c, userRoleMap(row))
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
