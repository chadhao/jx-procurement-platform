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
// ★ 数据源边界：全部断言基于 `t_user_role`（已配置角色者），**不是**飞书通讯录；
//   查不到角色 ⇒ 字段**留空**而非塞 open_id（用户明确反馈不要看到裸 ou_xxx）。

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

// TestOrgUsers_FiltersAndShape `/api/org/users`：字段形态、active 过滤、department/q 筛选。
func TestOrgUsers_FiltersAndShape(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_hao", Name: "郝端", Role: "采购经办人", Department: "江熙新材", Active: true},
		store.UserRole{OpenID: "ou_li", Name: "李四", Role: "申请人", Department: "生产部", Active: true},
		store.UserRole{OpenID: "ou_wang", Name: "王五", Role: "申请人", Department: "江熙新材", Active: true},
		// 停用者：不是有效审批人，不得出现在可选清单里。
		store.UserRole{OpenID: "ou_gone", Name: "老六", Role: "验收人", Department: "生产部", Active: false},
	)
	cookie := auth.Establish("ou_hao")

	// ① 全量：3 条启用者；每条含 open_id/name/role/department。
	rec, env := doRequest(e, http.MethodGet, "/api/org/users", cookie, "")
	if rec.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("全量清单 http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	items := mustData(t, env)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("启用者条数 = %d, 期望 3（停用者须被过滤）", len(items))
	}
	first := items[0].(map[string]any)
	for _, k := range []string{"open_id", "name", "role", "department"} {
		if _, ok := first[k]; !ok {
			t.Errorf("条目缺字段 %q: %v", k, first)
		}
	}

	// ② ?department= 精确筛选。
	_, env = doRequest(e, http.MethodGet, "/api/org/users?department=%E7%94%9F%E4%BA%A7%E9%83%A8", cookie, "")
	items = mustData(t, env)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["open_id"] != "ou_li" {
		t.Fatalf("department 筛选结果 = %v, 期望仅 ou_li", items)
	}

	// ③ ?q= 姓名模糊筛选。
	_, env = doRequest(e, http.MethodGet, "/api/org/users?q=%E9%83%9D", cookie, "")
	items = mustData(t, env)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "郝端" {
		t.Fatalf("q 筛选结果 = %v, 期望仅 郝端", items)
	}
}

// TestOrgDepartments_DedupeAndSort `/api/org/departments`：非空去重 + 稳定排序。
func TestOrgDepartments_DedupeAndSort(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_a", Name: "甲", Role: "申请人", Department: "生产部", Active: true},
		store.UserRole{OpenID: "ou_b", Name: "乙", Role: "验收人", Department: "江熙新材", Active: true},
		store.UserRole{OpenID: "ou_c", Name: "丙", Role: "申请人", Department: "生产部", Active: true},
		// 空部门不进清单；停用者的部门仍在（部门是客观事实，不随停用消失）。
		store.UserRole{OpenID: "ou_d", Name: "丁", Role: "申请人", Department: "", Active: true},
	)
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

// TestOrgEndpoints_EmptyTable 空表 ⇒ 空数组、不报错（前端给「暂无可选人员」提示）。
func TestOrgEndpoints_EmptyTable(t *testing.T) {
	e, _, auth, _ := newAdminTestApp(t)
	cookie := auth.Establish("ou_anyone") // 角色表为空也能建会话（dev 直连）
	for _, path := range []string{"/api/org/users", "/api/org/departments"} {
		rec, env := doRequest(e, http.MethodGet, path, cookie, "")
		if rec.Code != http.StatusOK || env.Code != 0 {
			t.Fatalf("%s: 空表应 200/code=0，实得 http=%d code=%d body=%s",
				path, rec.Code, env.Code, rec.Body.String())
		}
		items := mustData(t, env)["items"].([]any)
		if len(items) != 0 {
			t.Fatalf("%s: 空表应返回空数组，实得 %v（不造假数据）", path, items)
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
// ① 我的待办（/api/approval/tasks）与 ② 详情（/api/approval/{biz_no}）的 tasks[] 均携带；
// ③ t_user_role 查不到的人 ⇒ 字段**留空**，绝不塞 open_id（解绿框的核心回落语义）。
func TestApprovalTasks_CarryAssigneeInfo(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_me", Name: "郝端", Role: "采购经办人", Department: "江熙新材", Active: true},
		// ★ ou_ghost 故意不建角色映射 —— 断言「取不到 ⇒ 留空」。
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

	// ① 详情 tasks[]：有映射 ⇒ 姓名+部门；无映射 ⇒ 空串（绝不是 open_id）。
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
