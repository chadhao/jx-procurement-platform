package httpapi

// 角色代理人后台 CRUD（N-028 · spec/authority.json · docs/05-API §3.9 契约）。
//
// ★ 五条硬校验（checks 逐条）：
//   ① value_must_be_existing_user：agent_open_id 须在 t_org_user 镜像且**未离职**（40400 不存在 / 40000 已离职）
//   ② single_active_agent_per_role：每 role_key 至多 1 条 active（40900，部分唯一索引兜底）
//   ③ no_shared_agent_across_two_levels：合同链相邻两级（supervisor / project_general_manager）
//      代理人不得同一人（40000）
//   ④ role_key 白名单（authority.agent_eligible_roles.eligible；applicant/sys_admin/
//      group_finance/group_approval 天然不在名单 ⇒ 拒 40000）
//   ⑤ 每次成功变更写审计（role_agent_create / role_agent_update，条目前后值）；DELETE 永远 40900
// ★ 生效范围：备付金节点（approve_petty_cash/disburse）不接受代理人 —— 按**节点**排除
//   （chain.NodeAllowsAgent，M9 消费；此处 GET 响应回传 denied_node_ids 供前端标注）。
// ★ feature_enabled=false：正向消费端属 M9；页签须显式标注「代理人功能未启用」（README #24）。

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// roleAgentFeatureEnabled 正向消费端（代理人可转交/回退）随 M9 落地才置 true。
// 解除条件见 spec/authority.json#enable_guard.lifting —— 未解除前，任何操作
// **不得**因存在代理人记录而放行（服务端本就不读本表参与解析，见链侧负向守卫）。
const roleAgentFeatureEnabled = false

// adjacentAgentPair 合同链相邻两级（checks#no_shared_agent_across_two_levels 点名）。
var adjacentAgentPair = [2]string{"supervisor", "project_general_manager"}

func (d Deps) requireAuthority(c echo.Context) bool {
	if d.Spec == nil || d.Spec.Authority == nil {
		_ = fail(c, http.StatusServiceUnavailable, codeNotReady, "授权配置登记册未装配")
		return false
	}
	return true
}

// eligibleRoleKeys 白名单（spec 驱动）。
func (d Deps) eligibleRoleKeys() []string {
	if d.Spec == nil || d.Spec.Authority == nil {
		return nil
	}
	return d.Spec.Authority.Eligible.Eligible
}

// validateAgentOpenID ①：镜像存在且未离职。返回 (httpStatus, errMsg)；ok=true 表示通过。
func (d Deps) validateAgentOpenID(ctx context.Context, openID string) (int, string, bool) {
	if strings.TrimSpace(openID) == "" {
		return http.StatusBadRequest, "agent_open_id 不能为空", false
	}
	u, err := d.DB.GetOrgUser(ctx, openID)
	if err != nil || u == nil {
		return http.StatusNotFound, "open_id 不存在于通讯录镜像（禁止手填 —— 请从系统内点选）", false
	}
	if u.IsResigned || u.IsDeleted || u.IsExited || u.IsFrozen {
		return http.StatusBadRequest, "该用户已离职/不可用（不得配置为代理人）", false
	}
	return 0, "", true
}

// validateAdjacentAgent ③：合同链相邻两级代理人不得同一人。
// excludeID 用于 PUT 时忽略自身。
func (d Deps) validateAdjacentAgent(ctx context.Context, roleKey, agentOpenID string, excludeID int64) (int, string, bool) {
	var otherRole string
	switch roleKey {
	case adjacentAgentPair[0]:
		otherRole = adjacentAgentPair[1]
	case adjacentAgentPair[1]:
		otherRole = adjacentAgentPair[0]
	default:
		return 0, "", true // 不在相邻对内
	}
	other, err := d.DB.FindActiveRoleAgent(ctx, otherRole)
	if err != nil {
		return http.StatusInternalServerError, err.Error(), false
	}
	if other != nil && other.AgentOpenID == agentOpenID && other.ID != excludeID {
		return http.StatusBadRequest,
			"同一审批链相邻两级不得由同一人代理（合同两级会因此变一级）", false
	}
	return 0, "", true
}

// handleAdminRoleAgentsList GET /api/admin/role-agents?role_key=&status=
func (d Deps) handleAdminRoleAgentsList(c echo.Context) error {
	if _, granted := d.requireSysAdmin(c); !granted {
		return nil
	}
	if !d.requireAuthority(c) {
		return nil
	}
	rows, err := d.DB.ListRoleAgents(c.Request().Context(),
		strings.TrimSpace(c.QueryParam("role_key")), strings.TrimSpace(c.QueryParam("status")))
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, map[string]any{
			"id": r.ID, "role_key": r.RoleKey, "agent_open_id": r.AgentOpenID,
			"state": r.State, "note": r.Note,
		})
	}
	// spec 标记 agent_allowed=false 的节点不接受代理人 —— 回传供前端标注（按节点，不按角色）
	denied := make([]string, 0, 2)
	for id := range chain.AgentDeniedNodeIDs(d.Spec) {
		denied = append(denied, id)
	}
	return ok(c, map[string]any{
		"items":           items,
		"eligible_roles":  d.eligibleRoleKeys(),
		"feature_enabled": roleAgentFeatureEnabled, // false：M9 前正向消费端未实现（README #24）
		"feature_note":    "代理人功能未启用：本页仅登记配置；转交/回退按代理人待 M9 落地后生效",
		"denied_node_ids": denied,
		"adjacent_pair":   []string{adjacentAgentPair[0], adjacentAgentPair[1]},
	})
}

// handleAdminRoleAgentsCreate POST /api/admin/role-agents {role_key, agent_open_id, note?}
func (d Deps) handleAdminRoleAgentsCreate(c echo.Context) error {
	idn, granted := d.requireSysAdmin(c)
	if !granted {
		return nil
	}
	if !d.requireAuthority(c) {
		return nil
	}
	ctx := c.Request().Context()
	var req struct {
		RoleKey     string `json:"role_key"`
		AgentOpenID string `json:"agent_open_id"`
		Note        string `json:"note"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	req.RoleKey = strings.TrimSpace(req.RoleKey)
	req.AgentOpenID = strings.TrimSpace(req.AgentOpenID)

	// ④ 白名单（含 4 个排除角色 —— 不在 eligible 内天然被拒）
	if !d.Spec.Authority.IsEligibleRole(req.RoleKey) {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"role_key 不在可配置白名单内（applicant / sys_admin / group_finance / group_approval 不可配；"+req.RoleKey+" 亦不在 eligible）")
	}
	// ① 镜像存在且未离职
	if status, msg, ok2 := d.validateAgentOpenID(ctx, req.AgentOpenID); !ok2 {
		return fail(c, status, codeBadRequest, msg)
	}
	// ③ 相邻两级不同人
	if status, msg, ok2 := d.validateAdjacentAgent(ctx, req.RoleKey, req.AgentOpenID, 0); !ok2 {
		return fail(c, status, codeBadRequest, msg)
	}

	row, err := d.DB.InsertRoleAgent(ctx, req.RoleKey, req.AgentOpenID, req.Note, idn.OpenID)
	if err != nil {
		if err == store.ErrRoleAgentConflict {
			return fail(c, http.StatusConflict, codeConflict, "该角色已有生效代理人（每角色至多 1 条 active）")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "role_agent_create",
		Resource: "table:role_agent", TargetID: row.RoleKey, Result: "allow",
		DetailJSON: `{"role_key":"` + row.RoleKey + `","agent_open_id":"` + row.AgentOpenID + `","state":"active"}`,
	})
	return ok(c, map[string]any{"id": row.ID, "role_key": row.RoleKey,
		"agent_open_id": row.AgentOpenID, "state": row.State, "feature_enabled": roleAgentFeatureEnabled})
}

// handleAdminRoleAgentsUpdate PUT /api/admin/role-agents/:id {agent_open_id?, note?, state?}
func (d Deps) handleAdminRoleAgentsUpdate(c echo.Context) error {
	idn, granted := d.requireSysAdmin(c)
	if !granted {
		return nil
	}
	if !d.requireAuthority(c) {
		return nil
	}
	ctx := c.Request().Context()
	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的记录 id")
	}
	before, err := d.DB.GetRoleAgent(ctx, id)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "代理人记录不存在")
	}
	var req struct {
		AgentOpenID *string `json:"agent_open_id"`
		Note        *string `json:"note"`
		State       *string `json:"state"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	agent := ""
	if req.AgentOpenID != nil {
		agent = strings.TrimSpace(*req.AgentOpenID)
		if status, msg, ok2 := d.validateAgentOpenID(ctx, agent); !ok2 {
			return fail(c, status, codeBadRequest, msg)
		}
		if status, msg, ok2 := d.validateAdjacentAgent(ctx, before.RoleKey, agent, id); !ok2 {
			return fail(c, status, codeBadRequest, msg)
		}
	}
	state := ""
	if req.State != nil {
		state = strings.TrimSpace(*req.State)
		if state != "active" && state != "retired" {
			return fail(c, http.StatusBadRequest, codeBadRequest, "state 仅允许 active / retired（只停用不删）")
		}
	}
	note := ""
	if req.Note != nil {
		note = strings.TrimSpace(*req.Note)
	}
	row, err := d.DB.UpdateRoleAgent(ctx, id, agent, note, state, idn.OpenID)
	if err != nil {
		if err == store.ErrRoleAgentConflict {
			return fail(c, http.StatusConflict, codeConflict, "该角色已有生效代理人（每角色至多 1 条 active）")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "role_agent_update",
		Resource: "table:role_agent", TargetID: row.RoleKey, Result: "allow",
		DetailJSON: `{"before":{"agent_open_id":"` + before.AgentOpenID + `","state":"` + before.State +
			`"},"after":{"agent_open_id":"` + row.AgentOpenID + `","state":"` + row.State + `"}}`,
	})
	return ok(c, map[string]any{"id": row.ID, "role_key": row.RoleKey,
		"agent_open_id": row.AgentOpenID, "state": row.State})
}

// handleAdminRoleAgentsDelete DELETE /api/admin/role-agents/:id —— **永远 40900**。
// ★ 只停用不删：在办单据可能正处于该代理关系，删除会让「当时凭什么能审」无从追溯。
func (d Deps) handleAdminRoleAgentsDelete(c echo.Context) error {
	idn, granted := d.requireSysAdmin(c)
	if !granted {
		return nil
	}
	d.audit(c.Request().Context(), &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "role_agent_delete_refused",
		Resource: "table:role_agent", TargetID: c.Param("id"), Result: "deny",
		DetailJSON: `{"reason":"retire_only（authority.delete_policy）"}`,
	})
	return fail(c, http.StatusConflict, codeConflict,
		"代理人配置只停用不删除：请 PUT 将 state 置为 retired —— 在办单据的代理关系须可追溯")
}
