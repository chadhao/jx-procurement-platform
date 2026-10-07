package httpapi

// sys_role_n075_test.go —— N-075 系统角色独立成表（t_sys_role）验收判据 ①–④：
//
//	① 同一 open_id 同时持「项目总经理」（t_user_role）与「系统管理员」（t_sys_role）
//	   ⇒ 两行共存无 UNIQUE 冲突、requireSysAdmin 通过、台账可见性按项目总经理口径；
//	② 仅系统角色者 ⇒ 可进 /api/admin/*，但台账/看板 403（权限矩阵不认 SysRoles）；
//	③ 仅审批角色者（项目总经理）⇒ /api/admin/* 403（系统角色判定不再认 Role）；
//	④ 迁移 0022 重复执行结果一致；t_user_role 无「系统管理员」行、t_sys_role 有。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	"github.com/chadhao/jx-procurement-platform/migrations"
)

// TestSysRoleDualIdentityCoexistN075 判据①：核心场景（双身份共存 + 管理域通过 + 台账按审批角色口径）。
func TestSysRoleDualIdentityCoexistN075(t *testing.T) {
	e, db, auth, loader := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	// 郝端场景：t_user_role = 项目总经理（一人一审批角色 UNIQUE 满足）；
	// t_sys_role = 系统管理员（UNIQUE(open_id, role) 与审批角色正交）。
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_hao", Role: roleProjectGM, Active: true})
	seedSysAdmin(t, db, "ou_hao")

	// ①-a 两表各自可读、互不挤占（若共表则第二次写入必撞 open_id UNIQUE）。
	ur, err := db.GetUserRole(ctx, "ou_hao")
	if err != nil || ur.Role != roleProjectGM {
		t.Fatalf("审批角色半边丢失: ur=%+v err=%v（期望 Role=%s）", ur, err, roleProjectGM)
	}
	sysRoles, err := db.GetSysRoles(ctx, "ou_hao")
	if err != nil || len(sysRoles) != 1 || sysRoles[0] != roleSysAdmin {
		t.Fatalf("系统角色半边丢失: %v err=%v（期望 [%s]）", sysRoles, err, roleSysAdmin)
	}

	cookie := auth.Establish("ou_hao")
	// ①-b requireSysAdmin 通过（判定走 SysRoles）。
	rec, env := doRequest(e, http.MethodGet, "/api/admin/permission-rules", cookie, "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("双身份进管理域: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	// ①-c 台账可见性按「项目总经理」口径（矩阵只读 Role 半边 —— 与仅审批角色的项目总经理同规则）。
	recL, envL := doRequest(e, http.MethodGet, "/api/ledger/L01", cookie, "")
	if recL.Code != http.StatusOK || envL.Code != codeOK {
		t.Fatalf("双身份台账查询: http=%d code=%d body=%s", recL.Code, envL.Code, recL.Body.String())
	}
	// ①-d 矩阵规则＝项目总经理行（ALL、无静态列 deny —— seed.go#specs 现文；
	//     ★ 判据① 括号「不含金额列」与 specs 现文冲突〔禁金额的是旧系统管理员行，
	//       项目总经理不禁金额〕，断言以 specs 现文为准，提请 WB 复核判据文字）。
	rule, err := loader.Resolve(ctx, "ledger:L01", permission.Identity{
		OpenID: "ou_hao", Role: roleProjectGM, SysRoles: []string{roleSysAdmin},
	})
	if err != nil {
		t.Fatalf("解析规则失败: %v", err)
	}
	if string(rule.RowScope) != "ALL" || len(rule.ColumnDeny) != 0 {
		t.Errorf("双身份台账口径 = scope %q / deny %v, 期望项目总经理行（ALL、无列 deny）",
			rule.RowScope, rule.ColumnDeny)
	}
}

// TestSysRoleOnlyAdminYesLedgerNoN075 判据②：仅系统角色者 —— 管理域 200、台账/看板 403。
func TestSysRoleOnlyAdminYesLedgerNoN075(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedSysAdmin(t, db, "ou_ops") // 仅系统角色：t_user_role **不写**
	cookie := auth.Establish("ou_ops")

	// 管理域可进（identityFrom 对「无审批角色但有系统角色」不报未映射）。
	recA, envA := doRequest(e, http.MethodGet, "/api/admin/permission-rules", cookie, "")
	if recA.Code != http.StatusOK || envA.Code != codeOK {
		t.Fatalf("仅系统角色进管理域: http=%d code=%d body=%s", recA.Code, envA.Code, recA.Body.String())
	}
	// 台账 403（Role 为空 ⇒ 矩阵 DENY，deny by default 不变）。
	recL, _ := doRequest(e, http.MethodGet, "/api/ledger/L01", cookie, "")
	if recL.Code != http.StatusForbidden {
		t.Errorf("仅系统角色查台账: http=%d, 期望 403（body=%s）", recL.Code, recL.Body.String())
	}
	// 看板 403（存在性检查在权限前 ⇒ 用真实存在的看板 15；Role 空 ⇒ 矩阵 DENY 短路）。
	recD, _ := doRequest(e, http.MethodGet, "/api/dashboard/15", cookie, "")
	if recD.Code != http.StatusForbidden {
		t.Errorf("仅系统角色看看板: http=%d, 期望 403（body=%s）", recD.Code, recD.Body.String())
	}
}

// TestApprovalRoleOnlyAdminForbiddenN075 判据③：仅审批角色者（项目总经理）⇒ 管理域 403。
func TestApprovalRoleOnlyAdminForbiddenN075(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})
	cookie := auth.Establish("ou_gm")

	rec, _ := doRequest(e, http.MethodGet, "/api/admin/permission-rules", cookie, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("仅审批角色进管理域: http=%d, 期望 403（body=%s）", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "仅系统管理员") {
		t.Errorf("403 文案应为系统管理员专属提示, 实为: %s", rec.Body.String())
	}
}

// TestMigration0022SysRoleIdempotentN075 判据④：0022 重复执行结果一致；
// 迁移后 t_user_role 无「系统管理员」行、t_sys_role 有该行。
func TestMigration0022SysRoleIdempotentN075(t *testing.T) {
	db := storetest.NewDB(t) // 迁移已跑（0022 对空库 no-op）
	ctx := context.Background()

	// 铺「迁移前」状态：t_user_role 存在系统管理员行（0022 的迁入/删除对象）。
	if err := db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_legacy", Name: "旧管理员", Role: roleSysAdmin, Active: true,
	}); err != nil {
		t.Fatalf("铺存量行失败: %v", err)
	}
	body, err := migrations.FS.ReadFile("0022_sys_role.sql")
	if err != nil {
		t.Fatalf("读取 0022 失败: %v", err)
	}
	snapshot := func() (legacy, sysN int, sysActive int) {
		legacy = storetest.Count(t, db, `SELECT COUNT(*) FROM t_user_role WHERE role = ?`, roleSysAdmin)
		sysN = storetest.Count(t, db, `SELECT COUNT(*) FROM t_sys_role WHERE open_id = ?`, "ou_legacy")
		sysActive = storetest.Count(t, db,
			`SELECT COUNT(*) FROM t_sys_role WHERE open_id = ? AND role = ? AND active = 1`,
			"ou_legacy", roleSysAdmin)
		return
	}
	// 第 1 次执行：迁入 + 删除。
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("执行 0022 失败: %v", err)
	}
	l1, s1, a1 := snapshot()
	if l1 != 0 {
		t.Errorf("迁移后 t_user_role 系统管理员行 = %d, 期望 0", l1)
	}
	if s1 != 1 || a1 != 1 {
		t.Errorf("迁移后 t_sys_role 行 = %d（active=%d）, 期望 1/1", s1, a1)
	}
	// 第 2 次执行（幂等）：结果一致。
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("重复执行 0022 失败: %v", err)
	}
	l2, s2, a2 := snapshot()
	if l2 != l1 || s2 != s1 || a2 != a1 {
		t.Errorf("重复执行结果不一致: (%d,%d,%d) → (%d,%d,%d)", l1, s1, a1, l2, s2, a2)
	}
}

// TestSysRoleOnlyLoginAllowedN075 次级点（登录门 · handlers_biz#handleAuthCallback）：
// 仅系统角色者可登录（Q3 初始管理员只种 t_sys_role ⇒ 登录门不放行则「装完不能管」）；
// 两者皆空 ⇒ 仍 401（deny by default 不变）。
func TestSysRoleOnlyLoginAllowedN075(t *testing.T) {
	e, _, db := newAuthTestApp(t, &config.Env{DevMode: true}, nil)
	seedSysAdmin(t, db, "ou_ops")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/auth/feishu/callback?state=devlogi&open_id=ou_ops", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("仅系统角色者登录: http=%d, 期望 302（body=%s）", rec.Code, rec.Body.String())
	}
	if cookieValue(t, rec, access.CookieName) == "" {
		t.Error("仅系统角色者登录未建立会话 Cookie")
	}

	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet,
		"/auth/feishu/callback?state=devlogi&open_id=ou_nobody", nil))
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("两表皆空者登录: http=%d, 期望 401（deny by default 不变）", rec2.Code)
	}
}

// TestSysRoleOnlyAuditQueryN075 次级点（审计 · handleAuditLogs 的 Role ∪ SysRoles 放行）：
// 仅系统角色者可查审计；仅审批角色（申请人，矩阵 DENY）不可。
func TestSysRoleOnlyAuditQueryN075(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedSysAdmin(t, db, "ou_ops")
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_me", Role: "申请人", Active: true})

	recOps, envOps := doRequest(e, http.MethodGet, "/api/audit/logs", auth.Establish("ou_ops"), "")
	if recOps.Code != http.StatusOK || envOps.Code != codeOK {
		t.Errorf("仅系统角色查审计: http=%d code=%d body=%s", recOps.Code, envOps.Code, recOps.Body.String())
	}
	recMe, _ := doRequest(e, http.MethodGet, "/api/audit/logs", auth.Establish("ou_me"), "")
	if recMe.Code != http.StatusForbidden {
		t.Errorf("申请人查审计: http=%d, 期望 403（body=%s）", recMe.Code, recMe.Body.String())
	}
}

// TestSysRoleReimbursementReadN075 次级点（报销读白名单 · authorizeRole 的系统管理员槽位）：
// 白名单含系统管理员 ⇒ 系统角色满足该槽位（Role 里已无该值）。
func TestSysRoleReimbursementReadN075(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedSysAdmin(t, db, "ou_ops")
	rec, env := doRequest(e, http.MethodGet, "/api/reimbursement", auth.Establish("ou_ops"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Errorf("仅系统角色读报销跟踪: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
}

// TestApprovalVisibleToSysRolesN075 次级点（approvalVisibleTo 纯函数 · 第 5 点）：
// 系统角色半边走 SysRoles、不再认 Role 字面。
func TestApprovalVisibleToSysRolesN075(t *testing.T) {
	inst := &store.Instance{ApplicantOpenID: "ou_other"}
	// 仅系统角色（Role 空）⇒ 全量可见（管理域语义）。
	if !approvalVisibleTo(permission.Identity{SysRoles: []string{roleSysAdmin}}, inst, nil) {
		t.Error("仅系统角色者应可见实例（SysRoles 判定未生效）")
	}
	// 仅审批角色（非申请人、无任务）⇒ 不可见。
	if approvalVisibleTo(permission.Identity{Role: roleProjectGM}, inst, nil) {
		t.Error("仅审批角色者不应因 Role 字面获得全量可见")
	}
	// 陈旧 Role=系统管理员 字面值（0022 迁移后 t_user_role 不应再有）⇒ 不再放行。
	if approvalVisibleTo(permission.Identity{Role: roleSysAdmin}, inst, nil) {
		t.Error("approvalVisibleTo 仍认 Role 字面 —— 判定未改走 SysRoles")
	}
}
