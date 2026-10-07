package httpapi

// N-028 验收：五条硬约束各配「应拦 / 应放行」＋ 负向守卫（代理人不得参与解析）。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

func seedAgentOrgUsers(t *testing.T, db *store.DB) {
	t.Helper()
	ctx := context.Background()
	users := []store.OrgUser{
		{OpenID: "ou_agent_a", Name: "甲代理", FirstSeenAt: time.Now(), LastSeenAt: time.Now()},
		{OpenID: "ou_agent_b", Name: "乙代理", FirstSeenAt: time.Now(), LastSeenAt: time.Now()},
		{OpenID: "ou_gone", Name: "已离职", IsResigned: true, FirstSeenAt: time.Now(), LastSeenAt: time.Now()},
	}
	for i := range users {
		u := users[i]
		if err := db.UpsertOrgUser(ctx, &u); err != nil {
			t.Fatal(err)
		}
	}
}

func agentApp(t *testing.T) (*store.DB, func(string) string) {
	e, db, auth, _ := newAdminTestApp(t)
	_ = e
	seedSysAdmin(t, db, "ou_admin")
	seedAgentOrgUsers(t, db)
	return db, auth.Establish
}

// ① value_must_be_existing_user
func TestAgentValueMustBeExistingUser(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedSysAdmin(t, db, "ou_admin")
	seedAgentOrgUsers(t, db)
	admin := auth.Establish("ou_admin")

	// 拦：镜像不存在 → 40400
	rec, env := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_not_in_mirror"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("镜像不存在应 404，实为 %d（%s）", rec.Code, env.Message)
	}
	// 拦：已离职 → 40000
	rec, env = doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_gone"}`)
	if rec.Code != http.StatusBadRequest || env.Code != 40000 {
		t.Errorf("已离职应 400/40000，实为 %d/%d", rec.Code, env.Code)
	}
	// 放行：镜像内在职
	rec, env = doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_agent_a","note":"出差期间"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("在职用户配置应放行，实为 %d（%s）", rec.Code, env.Message)
	}
}

// ② single_active_agent_per_role
func TestAgentSingleActivePerRole(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedSysAdmin(t, db, "ou_admin")
	seedAgentOrgUsers(t, db)
	admin := auth.Establish("ou_admin")

	if rec, _ := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_agent_a"}`); rec.Code != http.StatusOK {
		t.Fatalf("首条应放行，实为 %d", rec.Code)
	}
	// 拦：同角色第二条 active
	rec, env := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_agent_b"}`)
	if rec.Code != http.StatusConflict || env.Code != 40900 {
		t.Errorf("同角色第二条应 409/40900，实为 %d/%d", rec.Code, env.Code)
	}
	// 放行：先停用旧的，再配新的（停用路径也算放行用例的一部分）
	var id int64
	if err := db.QueryRowContext(ctx2(), `SELECT id FROM t_role_agent WHERE role_key='supervisor' AND state='active'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if rec2, _ := doRequest(e, http.MethodPut, "/api/admin/role-agents/"+itoaTest(id), admin,
		`{"state":"retired"}`); rec2.Code != http.StatusOK {
		t.Fatalf("停用失败 %d", rec2.Code)
	}
	if rec3, _ := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_agent_b"}`); rec3.Code != http.StatusOK {
		t.Errorf("停用后新配应放行，实为 %d", rec3.Code)
	}
}

// ③ no_shared_agent_across_two_levels（合同链相邻两级）
func TestAgentAdjacentLevelsDistinct(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedSysAdmin(t, db, "ou_admin")
	seedAgentOrgUsers(t, db)
	admin := auth.Establish("ou_admin")

	// 放行：两级不同人
	if rec, _ := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_agent_a"}`); rec.Code != http.StatusOK {
		t.Fatalf("supervisor 配置失败 %d", rec.Code)
	}
	rec, env := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"project_general_manager","agent_open_id":"ou_agent_b"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("相邻两级不同人应放行，实为 %d（%s）", rec.Code, env.Message)
	}
	// 拦：换掉 pgm 的代理人为 supervisor 的同一人 —— 需先改 pgm 行
	var pgmID int64
	if err := db.QueryRowContext(ctx2(), `SELECT id FROM t_role_agent WHERE role_key='project_general_manager' AND state='active'`).Scan(&pgmID); err != nil {
		t.Fatal(err)
	}
	rec, env = doRequest(e, http.MethodPut, "/api/admin/role-agents/"+itoaTest(pgmID), admin,
		`{"agent_open_id":"ou_agent_a"}`)
	if rec.Code != http.StatusBadRequest || env.Code != 40000 {
		t.Errorf("相邻两级同人应 400/40000，实为 %d/%d（%s）", rec.Code, env.Code, env.Message)
	}
}

// ④ 白名单（4 个排除角色不可配）
func TestAgentEligibleWhitelist(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedSysAdmin(t, db, "ou_admin")
	seedAgentOrgUsers(t, db)
	admin := auth.Establish("ou_admin")

	for _, bad := range []string{"applicant", "sys_admin", "group_finance", "group_approval"} {
		rec, env := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
			`{"role_key":"`+bad+`","agent_open_id":"ou_agent_a"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("角色 %s 应拒 400，实为 %d（%s）", bad, rec.Code, env.Message)
		}
	}
	// 放行：eligible 角色（用 ops_supervisor）
	if rec, _ := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"ops_supervisor","agent_open_id":"ou_agent_a"}`); rec.Code != http.StatusOK {
		t.Errorf("eligible 角色应放行，实为 %d", rec.Code)
	}
}

// ⑤ 审计 + DELETE 永远 409 + GET 契约字段
func TestAgentAuditAndDeleteRefusedAndListContract(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedSysAdmin(t, db, "ou_admin")
	seedAgentOrgUsers(t, db)
	admin := auth.Establish("ou_admin")

	rec, _ := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_agent_a"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建失败 %d", rec.Code)
	}
	var id int64
	if err := db.QueryRowContext(ctx2(), `SELECT id FROM t_role_agent WHERE role_key='supervisor'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// DELETE → 40900（只停用不删）
	rec, env := doRequest(e, http.MethodDelete, "/api/admin/role-agents/"+itoaTest(id), admin, "")
	if rec.Code != http.StatusConflict || env.Code != 40900 {
		t.Errorf("DELETE 应 409/40900，实为 %d/%d", rec.Code, env.Code)
	}
	// 审计
	for _, action := range []string{"role_agent_create", "role_agent_delete_refused"} {
		var n int
		if err := db.QueryRowContext(ctx2(),
			`SELECT COUNT(*) FROM t_audit_log WHERE action=?`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("审计缺 %s", action)
		}
	}
	// GET 契约：eligible_roles 非空 / feature_enabled=true（N-072：M9 落地、解除条件已达成）/ denied 含两个备付金节点
	rec2, env2 := doRequest(e, http.MethodGet, "/api/admin/role-agents", admin, "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET 失败 %d", rec2.Code)
	}
	data, _ := env2.Data.(map[string]any)
	if data["feature_enabled"] != true {
		t.Errorf("feature_enabled 应为 true（enable_guard.lifting 解除条件已达成 · N-072），实为 %v", data["feature_enabled"])
	}
	// ★ T4③ 告示与事实同向（N-072）：note 的「已启用」表述必须与 feature_enabled 一致 ——
	// 改常量（false）⇒ 本组断言恰红；只改 note 回旧文案 ⇒ 同组恰红（与上一条断言口径隔离）。
	note, _ := data["feature_note"].(string)
	enabled, _ := data["feature_enabled"].(bool)
	noteSaysEnabled := strings.Contains(note, "已启用")
	if noteSaysEnabled != enabled {
		t.Errorf("告示与事实不同向：feature_enabled=%v 但 note 说已启用=%v，note=%q", enabled, noteSaysEnabled, note)
	}
	if strings.Contains(note, "未启用") {
		t.Errorf("feature_note 不得再称「未启用」（账实不符 · N-072），实为 %q", note)
	}
	if !strings.Contains(note, "备付金") && !strings.Contains(note, "加签") {
		t.Errorf("feature_note 须点明至少一条边界（备付金 / 加签），实为 %q", note)
	}
	if roles, _ := data["eligible_roles"].([]any); len(roles) == 0 {
		t.Error("eligible_roles 不应为空")
	}
	denied, _ := data["denied_node_ids"].([]any)
	got := map[string]bool{}
	for _, x := range denied {
		got[x.(string)] = true
	}
	if !got["approve_petty_cash"] || !got["disburse"] {
		t.Errorf("denied_node_ids = %v（应含两个备付金节点）", denied)
	}

	// ★ N-037：筛选参数统一为 `state`（spec/authority.json 字段名唯一真相）——
	//   `?state=active` 生效；旧名 `?status=` **不再被识别**（返回全量而非过滤结果）。
	rec3, env3 := doRequest(e, http.MethodGet, "/api/admin/role-agents?state=retired", admin, "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("?state=retired 失败 %d", rec3.Code)
	}
	if items, _ := env3.Data.(map[string]any)["items"].([]any); len(items) != 0 {
		t.Errorf("?state=retired 应过滤为 0 条（刚建的是 active），实为 %d", len(items))
	}
	// 旧名 status 不再识别：status=retired 若仍生效应返回 0 条；现应返回全量 1 条
	rec4, env4 := doRequest(e, http.MethodGet, "/api/admin/role-agents?status=retired", admin, "")
	if rec4.Code != http.StatusOK {
		t.Fatalf("?status= 失败 %d", rec4.Code)
	}
	if items, _ := env4.Data.(map[string]any)["items"].([]any); len(items) != 1 {
		t.Errorf("旧名 ?status=retired 应**不再过滤**（返回全量 1 条），实为 %d —— 双名未收敛或误认了旧参", len(items))
	}
}

// ⑥ 负向守卫：配了代理人后令本人不可用 ⇒ 解析仍 unresolved/block（不替补）
func TestAgentNeverSubstitutesInResolve(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_app", Role: "申请人", Department: "仓储部", Active: true},
		// ★ 关键：不配任何「主管领导」—— 本人不可用
	)
	seedSysAdmin(t, db, "ou_admin")
	seedAgentOrgUsers(t, db)
	admin := auth.Establish("ou_admin")
	// 为 supervisor 配生效代理人（配置存在，但绝不参与解析）
	if rec, _ := doRequest(e, http.MethodPost, "/api/admin/role-agents", admin,
		`{"role_key":"supervisor","agent_open_id":"ou_agent_a"}`); rec.Code != http.StatusOK {
		t.Fatalf("配置代理人失败 %d", rec.Code)
	}

	// 直接跑链解析（store-backed 适配器）：缺主管领导 ⇒ 必须 unresolved，不得落到 ou_agent_a
	bundle := metaTestBundle(t)
	svc := &chain.Service{B: bundle, Roles: chainRoleAdapterForTest{db: db}}
	amt := int64(100000)
	rc, err := svc.Compute(context.Background(), chain.Facts{
		DocType: chain.DocPR, AmountCents: &amt, UsageCategoryL1: "P01", Department: "仓储部",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Unresolved) == 0 {
		t.Fatal("本人不可用时解析应 unresolved（代理人不得替补）")
	}
	for _, n := range rc.Spec {
		for _, a := range n.Approvers {
			if a.OpenID == "ou_agent_a" {
				t.Errorf("代理人 %s 不得出现在审批人解析结果中（节点 %s）", a.OpenID, n.NodeID)
			}
		}
	}
	if err := rc.EnsureResolvable(); err == nil {
		t.Error("EnsureResolvable 应报错（40010 路径）")
	}
}

// ctx2 context.Background 短名（本文件查询用）。
func ctx2() context.Context { return context.Background() }
