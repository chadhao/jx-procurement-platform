package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// admin_users_mirror_test.go —— 「人员角色」页两处修复的回归（用户实测反馈 2026-10-14）：
//
//	① 列表以**通讯录镜像**为基：条数 = 同步报告「人员 N」，未绑角色者也出现且 role 为空
//	   （根因：旧实现以 t_user_role 为基 ⇒ 同步进来的新人不在列表里 ⇒ 无法给他分配角色）；
//	② 未绑角色者可**当场分配**（保存后直读库确认写入）；
//	③ 姓名 / 主部门**只读**：PATCH 带这两字段 ⇒ 显式 400 且值未被改动；
//	④ extra_depts（分管部门，我方业务配置）**仍可改**（确认没把它误锁）。
//
// ★ 单点变异（验收判据⑦）：把 ① 的查询改回「只查 t_user_role」⇒ **仅 ① 恰红**、
// ②③④ 保持绿（②③④ 走 PATCH/直读库，不依赖列表基）。
//
// ★ 镜像种子复用同包既有夹具 `seedOrgDept` / `seedOrgUser`（org_handlers_test.go，
// 经 Upsert 路径、与生产写入同构）——不另造一套，避免夹具漂移。

// TestAdminUsersListMatchesDirectory ① 列表条数 == 同步报告「人员 N」；未绑角色者出现且 role 为空。
func TestAdminUsersListMatchesDirectory(t *testing.T) {
	f := stubOrgFetcher{
		depts: []*store.OrgDepartment{
			{OpenDepartmentID: "od-prod", DepartmentID: "od-prod", ParentOpenDepartmentID: "0", Name: "生产部"},
		},
		usersByDept: map[string][]*store.OrgUser{
			"od-prod": {
				{OpenID: "ou_bound", Name: "甲", PrimaryDepartmentID: "od-prod", IsActivated: true},
				{OpenID: "ou_free1", Name: "乙", PrimaryDepartmentID: "od-prod", IsActivated: true},
				{OpenID: "ou_free2", Name: "丙", PrimaryDepartmentID: "od-prod", IsActivated: true},
			},
		},
	}
	e, db, auth := newAdminOrgSyncApp(t, f)
	seedSysAdmin(t, db, "ou_admin")
	// 一人预绑角色（"老成员"）；另两人未绑（"刚同步进来的新人"）——正是用户实测的场景。
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_bound", Role: "申请人", Active: true})
	cookie := auth.Establish("ou_admin")

	// 走本页「同步飞书通讯录」的同一后端入口，取同步报告。
	_, sEnv := doRequest(e, http.MethodPost, "/api/admin/org/sync", cookie, "")
	total := mustData(t, sEnv)["user_total"].(float64)

	rec, env := doRequest(e, http.MethodGet, "/api/admin/users", cookie, "")
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("列表 http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	items := mustData(t, env)["items"].([]any)
	if len(items) != int(total) || len(items) != 3 {
		t.Fatalf("列表条数 = %d，同步报告「人员 %v」，期望都是 3（未绑角色者也须出现）", len(items), total)
	}

	byID := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byID[m["open_id"].(string)] = m
	}
	// ★ 未绑角色者确实在列表里，且 role 为空（不是被过滤掉）。
	for _, id := range []string{"ou_free1", "ou_free2"} {
		m, ok := byID[id]
		if !ok {
			t.Fatalf("%s 未出现在列表 —— 新人无法分配角色（本 bug 的核心形态）", id)
		}
		if m["role"] != "" {
			t.Errorf("%s role = %v, 期望空串（未绑角色）", id, m["role"])
		}
		if m["active"] != nil || m["updated_at"] != nil {
			t.Errorf("%s 未绑角色应 active/updated_at 为 null（不编造），实得 %v / %v", id, m["active"], m["updated_at"])
		}
	}
	// 已绑角色者的姓名/主部门取镜像（而非忽略）。
	if m := byID["ou_bound"]; m["role"] != "申请人" || m["name"] != "甲" || m["department"] != "生产部" {
		t.Errorf("ou_bound 行不符: %v", m)
	}
	// 未绑角色者的姓名/主部门同样取镜像（这正是"以镜像为基"的意义）。
	if m := byID["ou_free1"]; m["name"] != "乙" || m["department"] != "生产部" {
		t.Errorf("未绑角色者姓名/主部门应取镜像: %v", m)
	}

	// 离职/注销者（is_deleted=1）不得出现（否则列表会把已走的人也算进来）。
	seedOrgUser(t, db, store.OrgUser{
		OpenID: "ou_gone", Name: "丁", PrimaryDepartmentID: "od-prod",
		IsResigned: true, IsDeleted: true, EmployeeStatus: "离职",
	})
	_, env3 := doRequest(e, http.MethodGet, "/api/admin/users", cookie, "")
	if n := len(mustData(t, env3)["items"].([]any)); n != 3 {
		t.Errorf("离职者被列入人员角色页: 条数=%d, 期望 3", n)
	}
}

// TestAdminUnboundUserAssignable ② 未绑角色者（镜像在册）可分配角色 ⇒ 保存 ⇒ 直读库确认写入。
func TestAdminUnboundUserAssignable(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	seedSysAdmin(t, db, "ou_admin")
	seedOrgDept(t, db, "od-prod", "生产部", "0")
	seedOrgUser(t, db, store.OrgUser{OpenID: "ou_new", Name: "乙", PrimaryDepartmentID: "od-prod"})
	cookie := auth.Establish("ou_admin")

	// 首次分配（不传 active ⇒ 后端默认启用，避免写出 active=false 的"假分配"）。
	rec, env := doRequest(e, http.MethodPatch, "/api/admin/users/ou_new", cookie,
		`{"role":"申请人","extra_depts":["销售部"]}`)
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("未绑角色者分配失败: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}

	// 重查（直读库，不依赖前端渲染）：确实写入。
	row, err := db.GetUserRoleAny(ctx, "ou_new")
	if err != nil {
		t.Fatalf("重查角色失败: %v", err)
	}
	if row.Role != "申请人" {
		t.Errorf("role = %q, 期望 申请人（未写入）", row.Role)
	}
	if !row.Active {
		t.Error("首次分配应默认启用（active=true），否则新人仍不可用")
	}
	if len(row.ExtraDepts) != 1 || row.ExtraDepts[0] != "销售部" {
		t.Errorf("extra_depts = %v, 期望 [销售部]", row.ExtraDepts)
	}
	// 姓名/主部门以镜像播种（不是空串——否则会打空其它展示路径）。
	if row.Name != "乙" || row.Department != "生产部" {
		t.Errorf("name/department = %q/%q, 期望 乙/生产部（镜像值）", row.Name, row.Department)
	}

	// 不在通讯录在册者 ⇒ 404（不静默造出"系统里查无此人"的映射）。
	rec2, env2 := doRequest(e, http.MethodPatch, "/api/admin/users/ou_ghost", cookie, `{"role":"申请人"}`)
	if rec2.Code != http.StatusNotFound || env2.Code != codeNotFound {
		t.Errorf("不在册者应 404/40400: http=%d code=%d", rec2.Code, env2.Code)
	}
}

// TestAdminUserNameDepartmentReadOnly ③ 姓名/主部门只读（PATCH 显式 400 且不改值）；④ extra_depts 仍可改。
func TestAdminUserNameDepartmentReadOnly(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	seedSysAdmin(t, db, "ou_admin")
	seedOrgDept(t, db, "od-prod", "生产部", "0")
	seedOrgUser(t, db, store.OrgUser{OpenID: "ou_a", Name: "甲", PrimaryDepartmentID: "od-prod"})
	seedDefaultUsers(t, db, store.UserRole{
		OpenID: "ou_a", Name: "甲", Role: "申请人", Department: "生产部", Active: true,
	})
	cookie := auth.Establish("ou_admin")

	// ③ 带 name + department ⇒ 400（显式拒绝，连写路径都不进）。
	rec, env := doRequest(e, http.MethodPatch, "/api/admin/users/ou_a", cookie,
		`{"name":"改名","department":"改部门"}`)
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Fatalf("PATCH 带 name/department 应 400/40000: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	row, err := db.GetUserRoleAny(ctx, "ou_a")
	if err != nil {
		t.Fatalf("重查失败: %v", err)
	}
	if row.Name != "甲" || row.Department != "生产部" {
		t.Errorf("拒绝后值仍被改动: name=%q department=%q（期望 甲/生产部 不变）", row.Name, row.Department)
	}

	// 单独带 name 也须拒绝（不是只挡"同时带两个"）。
	recN, _ := doRequest(e, http.MethodPatch, "/api/admin/users/ou_a", cookie, `{"name":"改名"}`)
	if recN.Code != http.StatusBadRequest {
		t.Errorf("单独带 name 应 400, 实得 %d", recN.Code)
	}

	// ④ extra_depts 仍可改（我方业务配置，未误锁）。
	rec2, env2 := doRequest(e, http.MethodPatch, "/api/admin/users/ou_a", cookie, `{"extra_depts":["销售部"]}`)
	if rec2.Code != http.StatusOK || env2.Code != 0 {
		t.Fatalf("改 extra_depts 失败: http=%d code=%d body=%s", rec2.Code, env2.Code, rec2.Body.String())
	}
	row2, err := db.GetUserRoleAny(ctx, "ou_a")
	if err != nil {
		t.Fatalf("重查失败: %v", err)
	}
	if len(row2.ExtraDepts) != 1 || row2.ExtraDepts[0] != "销售部" {
		t.Errorf("extra_depts 未写入: %v", row2.ExtraDepts)
	}
	// 改 extra_depts 不应连带改姓名/主部门。
	if row2.Name != "甲" || row2.Department != "生产部" {
		t.Errorf("extra_depts 保存误伤了姓名/主部门: %q/%q", row2.Name, row2.Department)
	}
}
