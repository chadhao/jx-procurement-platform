package httpapi

// me_sysroles_n076_test.go —— N-076：`/api/me` 契约补 `sys_roles`（前端可见性数据源）。
// 判据① 郝端双身份返回 sys_roles 含「系统管理员」；② 仅审批角色者 sys_roles 空数组＋/admin 403；
// ③ 仅系统角色者 200（★ 改前 handleMe 直接解引用 nil ur ⇒ 500，本用例先红）＋/admin 可进；
// ④ 兼容：t_user_role 残留「系统管理员」值原样回传（前端 role 分支与 sys_roles 分支等价的后端半边）。

import (
	"net/http"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// n076SysRoles 从 /api/me 的 data 取 sys_roles（断言键存在且为数组 —— 缺键即红）。
func n076SysRoles(t *testing.T, data map[string]any) []string {
	t.Helper()
	raw, ok := data["sys_roles"].([]any)
	if !ok {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		t.Fatalf("GET /api/me 缺 sys_roles 数组键（N-076 契约）: keys=%v", keys)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// TestMeSysRolesDualN076 判据①：郝端（项目总经理 + 系统管理员）⇒ sys_roles 含系统管理员。
func TestMeSysRolesDualN076(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_hao", Role: roleProjectGM, Active: true})
	seedSysAdmin(t, db, "ou_hao")

	rec, env := doRequest(e, http.MethodGet, "/api/me", auth.Establish("ou_hao"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("双身份 /api/me: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	sysRoles := n076SysRoles(t, data)
	found := false
	for _, r := range sysRoles {
		if r == roleSysAdmin {
			found = true
		}
	}
	if !found {
		t.Errorf("sys_roles = %v, 期望含 %q", sysRoles, roleSysAdmin)
	}
	if data["role"] != roleProjectGM {
		t.Errorf("role = %v, 期望 项目总经理（审批角色半边不受影响）", data["role"])
	}
}

// TestMeSysRolesApprovalOnlyN076 判据②：仅审批角色（验收人）⇒ sys_roles 空数组 ＋ /admin 403。
func TestMeSysRolesApprovalOnlyN076(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_v", Role: "验收人", Active: true})

	rec, env := doRequest(e, http.MethodGet, "/api/me", auth.Establish("ou_v"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("验收人 /api/me: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if sys := n076SysRoles(t, data); len(sys) != 0 {
		t.Errorf("仅审批角色者 sys_roles = %v, 期望 []", sys)
	}
	recA, _ := doRequest(e, http.MethodGet, "/api/admin/permission-rules", auth.Establish("ou_v"), "")
	if recA.Code != http.StatusForbidden {
		t.Errorf("验收人直达 /admin: http=%d, 期望 403（body=%s）", recA.Code, recA.Body.String())
	}
}

// TestMeSysRolesOnlyN076 判据③：仅系统角色者 ⇒ /api/me 200（★ 改前 handleMe 解引用
// nil ur ⇒ 500 —— 先红点）＋ sys_roles 含系统管理员 ＋ /admin 可进。
func TestMeSysRolesOnlyN076(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedSysAdmin(t, db, "ou_ops")
	cookie := auth.Establish("ou_ops")

	rec, env := doRequest(e, http.MethodGet, "/api/me", cookie, "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("仅系统角色 /api/me: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	sys := n076SysRoles(t, data)
	if len(sys) != 1 || sys[0] != roleSysAdmin {
		t.Errorf("仅系统角色 sys_roles = %v, 期望 [%s]", sys, roleSysAdmin)
	}
	if data["role"] != "" {
		t.Errorf("仅系统角色 role = %v, 期望空串（t_user_role 无行）", data["role"])
	}
	recA, envA := doRequest(e, http.MethodGet, "/api/admin/permission-rules", cookie, "")
	if recA.Code != http.StatusOK || envA.Code != codeOK {
		t.Errorf("仅系统角色进管理域: http=%d code=%d body=%s", recA.Code, envA.Code, recA.Body.String())
	}
}

// TestMeSysRolesLegacyRoleN076 判据④（后端半边）：t_user_role 残留「系统管理员」值
// 原样回传 ＋ sys_roles 键仍在（空数组）—— 前端 role 分支与 sys_roles 分支取值等价。
func TestMeSysRolesLegacyRoleN076(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	// 直写残留值（validRole 已挡 API 路径；0022 迁移前的历史库形态）。
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_legacy", Role: roleSysAdmin, Active: true})

	rec, env := doRequest(e, http.MethodGet, "/api/me", auth.Establish("ou_legacy"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("残留值 /api/me: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if data["role"] != roleSysAdmin {
		t.Errorf("role = %v, 期望残留值 %q 原样回传", data["role"], roleSysAdmin)
	}
	if sys := n076SysRoles(t, data); len(sys) != 0 {
		t.Errorf("残留值者 sys_roles = %v, 期望 []（兼容走 role 分支）", sys)
	}
}
