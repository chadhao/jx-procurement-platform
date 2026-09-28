package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// org_handlers_test.go —— 组织查询接口（GET /api/org/users · /api/org/departments）
// 与任务列表「办理人姓名/部门」字段（解绿框）的行为测试。
//
// ★ 契约锚：docs/05-API.md §3.13 / §3.15。
// ★★ 数据源边界（2026-09-28 切换）：组织查询读**通讯录镜像**（t_org_user/t_org_department，
//   全员覆盖、含未配角色者）；role 字段来自 t_user_role（LEFT JOIN，未配角色者＝空串）；
//   办理人展示为两级：t_user_role → 镜像兜底（不过滤 is_deleted，docs/08 C-E 历史解析口径）。
//   查不到 ⇒ 字段**留空**而非塞 open_id（用户明确反馈不要看到裸 ou_xxx）。

// seedOrgDept / seedOrgUser 镜像种子（经 Upsert 路径，与生产写入同构）。
func seedOrgDept(t *testing.T, db *store.DB, id, name, parent string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.UpsertOrgDepartment(context.Background(), &store.OrgDepartment{
		OpenDepartmentID: id, DepartmentID: id, ParentOpenDepartmentID: parent,
		Name: name, FirstSeenAt: now, LastSeenAt: now, UpdatedAt: now, Source: "full",
	}); err != nil {
		t.Fatalf("写入部门镜像失败: %v", err)
	}
}

func seedOrgUser(t *testing.T, db *store.DB, u store.OrgUser) {
	t.Helper()
	now := time.Now().UTC()
	if u.FirstSeenAt.IsZero() {
		u.FirstSeenAt = now
	}
	u.LastSeenAt, u.UpdatedAt, u.Source = now, now, "full"
	if err := db.UpsertOrgUser(context.Background(), &u); err != nil {
		t.Fatalf("写入人员镜像失败: %v", err)
	}
}

// seedFlowTask 直接落一条 t_flow_task 行（测试专用；经 UpsertFlowTaskTx 事务路径）。
func seedFlowTask(t *testing.T, db *store.DB, task store.FlowTask) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	if err := db.UpsertFlowTaskTx(ctx, tx, &task); err != nil {
		_ = tx.Rollback()
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}
}

// seedPendingInstance 落一条 PENDING 实例（我的待办列表只扫 PENDING 实例）。
func seedPendingInstance(t *testing.T, db *store.DB, code, bizNo, applicant, dept string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.UpsertInstance(context.Background(), &store.Instance{
		InstanceCode: code, ApprovalCode: "ac-org-test", DocType: "PR", BizNo: bizNo,
		Status: "PENDING", StatusRaw: "PENDING", ApplicantOpenID: applicant,
		Department: dept, Source: "event", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入实例失败: %v", err)
	}
}

// TestOrgUsers_MirrorSource `/api/org/users`：镜像数据源、字段形态、department/q 筛选、
// 未配角色者**不过滤**（role 留空）、离职者不可见。
func TestOrgUsers_MirrorSource(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedOrgDept(t, db, "od-hx", "江熙新材", "0")
	seedOrgDept(t, db, "od-prod", "生产部", "0")
	// 在用人员（前三位已配角色，最后一位未配角色——全员覆盖的关键断言点）。
	seedOrgUser(t, db, store.OrgUser{OpenID: "ou_hao", Name: "郝端", PrimaryDepartmentID: "od-hx"})
	seedOrgUser(t, db, store.OrgUser{OpenID: "ou_li", Name: "李四", PrimaryDepartmentID: "od-prod"})
	seedOrgUser(t, db, store.OrgUser{OpenID: "ou_wang", Name: "王五", PrimaryDepartmentID: "od-hx"})
	seedOrgUser(t, db, store.OrgUser{OpenID: "ou_norole", Name: "赵六", PrimaryDepartmentID: "od-prod"})
	// 离职者：is_resigned=1 ⇒ 全量同步置 is_deleted=1，不得出现在可选清单。
	seedOrgUser(t, db, store.OrgUser{OpenID: "ou_gone", Name: "老六", PrimaryDepartmentID: "od-prod",
		IsResigned: true, IsDeleted: true, EmployeeStatus: "离职"})
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_hao", Name: "郝端", Role: "采购经办人", Department: "江熙新材", Active: true},
		store.UserRole{OpenID: "ou_li", Name: "李四", Role: "申请人", Department: "生产部", Active: true},
		store.UserRole{OpenID: "ou_wang", Name: "王五", Role: "申请人", Department: "江熙新材", Active: true},
	)
	cookie := auth.Establish("ou_hao")

	// ① 全量：4 条在用者（含未配角色者；离职者被过滤）；每条含 open_id/name/role/department。
	rec, env := doRequest(e, http.MethodGet, "/api/org/users", cookie, "")
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("全量清单 http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	items := mustData(t, env)["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("条数 = %d, 期望 4（未配角色者不过滤、离职者过滤）", len(items))
	}
	first := items[0].(map[string]any)
	for _, k := range []string{"open_id", "name", "role", "department"} {
		if _, ok := first[k]; !ok {
			t.Errorf("条目缺字段 %q: %v", k, first)
		}
	}
	// ② 未配角色者在列且 role 为空串（镜像 ≠ 权限：role 不伪造）。
	byID := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byID[m["open_id"].(string)] = m
	}
	if v, ok := byID["ou_norole"]; !ok {
		t.Fatalf("未配角色者 ou_norole 应在清单中（数据源＝飞书全员）")
	} else if v["role"] != "" {
		t.Errorf("未配角色者 role 应为空串，实得 %v", v["role"])
	} else if v["department"] != "生产部" {
		t.Errorf("未配角色者 department 应来自镜像（生产部），实得 %v", v["department"])
	}
	// ③ 已配角色者 role 来自 t_user_role。
	if byID["ou_hao"]["role"] != "采购经办人" {
		t.Errorf("ou_hao role = %v, 期望 采购经办人", byID["ou_hao"]["role"])
	}

	// ④ ?department= 精确筛选（主部门名）。
	_, env = doRequest(e, http.MethodGet, "/api/org/users?department=%E7%94%9F%E4%BA%A7%E9%83%A8", cookie, "")
	items = mustData(t, env)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("department 筛选结果条数 = %d, 期望 2（李四+赵六）", len(items))
	}

	// ⑤ ?q= 姓名模糊筛选。
	_, env = doRequest(e, http.MethodGet, "/api/org/users?q=%E9%83%9D", cookie, "")
	items = mustData(t, env)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "郝端" {
		t.Fatalf("q 筛选结果 = %v, 期望仅 郝端", items)
	}
}

// TestOrgDepartments_MirrorSource `/api/org/departments`：镜像来源、非空去重 + 稳定排序。
func TestOrgDepartments_MirrorSource(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedOrgDept(t, db, "od-hx", "江熙新材", "0")
	seedOrgDept(t, db, "od-prod", "生产部", "0")
	seedOrgDept(t, db, "od-empty", "", "0")  // 名称空（字段权限缺失情形）→ 不进清单
	seedOrgDept(t, db, "od-del", "已删部", "0") // 软删部门 → 不进清单
	if _, err := db.SoftDeleteOrgDepartmentsExcept(context.Background(),
		map[string]bool{"od-hx": true, "od-prod": true, "od-empty": true}, time.Now().UTC()); err != nil {
		t.Fatalf("软删失败: %v", err)
	}
	cookie := auth.Establish("ou_a")
	_, env := doRequest(e, http.MethodGet, "/api/org/departments", cookie, "")
	if env.Code != 0 {
		t.Fatalf("code=%d body=%s", env.Code, env.Message)
	}
	raw := mustData(t, env)["items"].([]any)
	var got []string
	for _, v := range raw {
		got = append(got, v.(string))
	}
	want := []string{"江熙新材", "生产部"} // 去重 + 字典序稳定排序
	if len(got) != len(want) {
		t.Fatalf("部门清单 = %v, 期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("部门清单 = %v, 期望 %v（去重 + 稳定排序）", got, want)
		}
	}
}

// TestOrgEndpoints_EmptyTable 镜像为空（尚未同步）⇒ 空数组、不报错。
func TestOrgEndpoints_EmptyTable(t *testing.T) {
	e, _, auth, _ := newAdminTestApp(t)
	cookie := auth.Establish("ou_anyone")
	for _, path := range []string{"/api/org/users", "/api/org/departments"} {
		rec, env := doRequest(e, http.MethodGet, path, cookie, "")
		if rec.Code != http.StatusOK || env.Code != 0 {
			t.Fatalf("%s: 空镜像应 200/code=0，实得 http=%d code=%d body=%s",
				path, rec.Code, env.Code, rec.Body.String())
		}
		items := mustData(t, env)["items"].([]any)
		if len(items) != 0 {
			t.Fatalf("%s: 空镜像应返回空数组，实得 %v（不造假数据）", path, items)
		}
	}
}

// TestOrgEndpoints_RequireSession 两者均属会话域：未登录 ⇒ 401/40100。
func TestOrgEndpoints_RequireSession(t *testing.T) {
	e, _, _, _ := newAdminTestApp(t)
	for _, path := range []string{"/api/org/users", "/api/org/departments"} {
		rec, env := doRequest(e, http.MethodGet, path, "", "")
		if rec.Code != http.StatusUnauthorized || env.Code != codeUnauthorized {
			t.Errorf("%s 未登录: http=%d code=%d, 期望 401/40100", path, rec.Code, env.Code)
		}
	}
}

// TestApprovalTasks_CarryAssigneeInfo 任务列表补 `assignee_name` / `assignee_department`：
// ① 详情 tasks[] 与 ② 我的待办均携带；③ 两级数据源都查不到的人 ⇒ 字段**留空**，
// 绝不塞 open_id（解绿框的核心回落语义）。
func TestApprovalTasks_CarryAssigneeInfo(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_me", Name: "郝端", Role: "采购经办人", Department: "江熙新材", Active: true},
		// ★ ou_ghost 故意不在 t_user_role、也**不在镜像** —— 断言「两级都查不到 ⇒ 留空」。
	)
	seedPendingInstance(t, db, "I-ORG1", "PR-2609-ORG1", "ou_me", "江熙新材")
	now := time.Now().UTC()
	seedFlowTask(t, db, store.FlowTask{
		TaskID: "PR-2609-ORG1-N1-ou_me-1-1", BizNo: "PR-2609-ORG1",
		NodeID: "N1", NodeName: "采购经办", NodeSeq: 1,
		AssigneeOpenID: "ou_me", Status: "PENDING", ReleaseState: "RELEASED",
		TaskOrder: 1, CreatedAt: now, UpdatedAt: now,
	})
	seedFlowTask(t, db, store.FlowTask{
		TaskID: "PR-2609-ORG1-N2-ou_ghost-1-1", BizNo: "PR-2609-ORG1",
		NodeID: "N2", NodeName: "领导审批", NodeSeq: 2,
		AssigneeOpenID: "ou_ghost", Status: "PENDING", ReleaseState: "RELEASED",
		TaskOrder: 1, CreatedAt: now, UpdatedAt: now,
	})
	cookie := auth.Establish("ou_me")

	// ① 详情 tasks[]：有映射 ⇒ 姓名+部门；两级都查不到 ⇒ 空串（绝不是 open_id）。
	rec, env := doRequest(e, http.MethodGet, "/api/approval/PR-2609-ORG1", cookie, "")
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("详情 http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	tasks := mustData(t, env)["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("任务条数 = %d, 期望 2", len(tasks))
	}
	byTask := map[string]map[string]any{}
	for _, it := range tasks {
		m := it.(map[string]any)
		byTask[m["task_id"].(string)] = m
	}
	mine := byTask["PR-2609-ORG1-N1-ou_me-1-1"]
	if mine["assignee_name"] != "郝端" || mine["assignee_department"] != "江熙新材" {
		t.Errorf("有映射任务: name=%v dept=%v, 期望 郝端/江熙新材", mine["assignee_name"], mine["assignee_department"])
	}
	ghost := byTask["PR-2609-ORG1-N2-ou_ghost-1-1"]
	if ghost["assignee_name"] != "" || ghost["assignee_department"] != "" {
		t.Errorf("无映射任务应留空（绝不塞 open_id）: name=%q dept=%q",
			ghost["assignee_name"], ghost["assignee_department"])
	}
	if ghost["assignee_name"] == "ou_ghost" {
		t.Errorf("红线：open_id 被塞进了 assignee_name")
	}

	// ② 我的待办条目同样携带（assignee＝我）。
	_, env = doRequest(e, http.MethodGet, "/api/approval/tasks", cookie, "")
	if env.Code != 0 {
		t.Fatalf("待办 code=%d body=%s", env.Code, env.Message)
	}
	items := mustData(t, env)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("待办条数 = %d, 期望 1", len(items))
	}
	item := items[0].(map[string]any)
	if item["assignee_name"] != "郝端" || item["assignee_department"] != "江熙新材" {
		t.Errorf("待办条目: name=%v dept=%v, 期望 郝端/江熙新材", item["assignee_name"], item["assignee_department"])
	}
}

// TestApprovalTasks_MirrorFallback 办理人只在镜像、未配角色 ⇒ 姓名/部门来自镜像兜底
// （数据源切换的意义：办理人显示不再依赖人工先配角色）。
func TestApprovalTasks_MirrorFallback(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedOrgDept(t, db, "od-fb", "质检部", "0")
	seedOrgUser(t, db, store.OrgUser{OpenID: "ou_qc", Name: "钱七", PrimaryDepartmentID: "od-fb"})
	// 请求者本身须有角色映射（详情接口走准入）；被展示的 ou_qc **不配角色**——
	// 断言其姓名/部门由镜像兜底（数据源切换的意义：显示不再依赖人工先配角色）。
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_admin", Name: "管理员", Role: "采购经办人", Department: "江熙新材", Active: true},
	)
	seedPendingInstance(t, db, "I-ORG2", "PR-2609-ORG2", "ou_admin", "质检部")
	now := time.Now().UTC()
	seedFlowTask(t, db, store.FlowTask{
		TaskID: "PR-2609-ORG2-N1-ou_qc-1-1", BizNo: "PR-2609-ORG2",
		NodeID: "N1", NodeName: "质检", NodeSeq: 1,
		AssigneeOpenID: "ou_qc", Status: "PENDING", ReleaseState: "RELEASED",
		TaskOrder: 1, CreatedAt: now, UpdatedAt: now,
	})
	cookie := auth.Establish("ou_admin")
	rec, env := doRequest(e, http.MethodGet, "/api/approval/PR-2609-ORG2", cookie, "")
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("详情 http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	tasks := mustData(t, env)["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("任务条数 = %d, 期望 1", len(tasks))
	}
	m := tasks[0].(map[string]any)
	if m["assignee_name"] != "钱七" || m["assignee_department"] != "质检部" {
		t.Errorf("镜像兜底: name=%v dept=%v, 期望 钱七/质检部", m["assignee_name"], m["assignee_department"])
	}
}
