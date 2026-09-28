package orgsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// orgsync_test.go —— 全量同步行为测试（httptest 模拟通讯录响应，不依赖真机）。
//
// 覆盖（对应任务自验硬要求 / docs/08 §7）：
//   - 分页：has_more 多页遍历正确、page_token 透传、has_more=true 无 page_token ⇒ 显式报错；
//   - 递归子部门：fetch_child=true 一次拉全 + 根部门补齐；
//   - 幂等：连跑两次不产生重复行；消失的人/部门标软删而非物理删；复活；
//   - 失败可见：中途中止/权限错 ⇒ 明确错误 + failed 流水 + last_full_error，不静默半成功，
//     last_full_success_at 不推进（N6）；
//   - 字段缺口：name/department_ids 缺失计入 field_gaps（N7）；
//   - 离职：is_resigned ⇒ is_deleted=1（软删、行保留）。

// fakeToken 恒定 token（模拟器不校验值）。
func fakeToken(context.Context) (string, error) { return "t-test", nil }

// dept / user 构造器（模拟器响应项）。
func dept(id, name, parent string) map[string]any {
	return map[string]any{"open_department_id": id, "department_id": id, "name": name,
		"parent_department_id": parent}
}

func user(id, name string, deptIDs []string, extra ...any) map[string]any {
	m := map[string]any{"open_id": id, "name": name, "department_ids": deptIDs,
		"status": map[string]bool{"is_activated": true}}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

// env0 官方统一包裹。
func env0(data map[string]any) map[string]any {
	return map[string]any{"code": 0, "msg": "success", "data": data}
}

// writeJSON 写 JSON 响应。
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("写响应失败: %v", err)
	}
}

// newTestRunner 起一个模拟飞书服务器 + 真实 SQLite 的 Runner。
//   - depts：递归部门列表（单页返回全部；root=0 不必包含，由 fetcher 补齐断言）；
//   - usersByDept：open_department_id → 用户列表；
//   - deptPageSplit / userPageSplit：把部门/用户列表切成多页（测 has_more 遍历）；
//   - failCode：非 0 时部门列表接口直接返回该错误码（测失败可见）。
func newTestRunner(t *testing.T, depts []map[string]any, usersByDept map[string][]map[string]any,
	failCode int, userFailCode int) (*Runner, *store.DB, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/open-apis/contact/v3/departments":
			if q.Get("fetch_child") != "true" || q.Get("department_id_type") != "open_department_id" {
				writeJSON(t, w, map[string]any{"code": 40001, "msg": "unexpected params"})
				return
			}
			if failCode != 0 {
				writeJSON(t, w, map[string]any{"code": failCode, "msg": "no dept authority error"})
				return
			}
			writeJSON(t, w, env0(map[string]any{"has_more": false, "items": depts}))
		case "/open-apis/contact/v3/users/find_by_department":
			deptID := q.Get("department_id")
			if q.Get("department_id_type") != "open_department_id" {
				writeJSON(t, w, map[string]any{"code": 40001, "msg": "unexpected params"})
				return
			}
			if userFailCode != 0 {
				writeJSON(t, w, map[string]any{"code": userFailCode, "msg": "no user authority error"})
				return
			}
			writeJSON(t, w, env0(map[string]any{"has_more": false, "items": usersByDept[deptID]}))
		default:
			writeJSON(t, w, map[string]any{"code": 404, "msg": "unknown path " + r.URL.Path})
		}
	}))
	t.Cleanup(srv.Close)
	db := storetest.NewDB(t)
	f := NewFeishuFetcher(TokenSourceFunc(fakeToken), srv.URL, slog.Default())
	return NewRunner(f, db, observ.NewMetrics(), observ.NewHealth("test"),
		observ.NewLogger("error", nil), time.Hour), db, srv
}

// orgRows 镜像行数。
func orgRows(t *testing.T, db *store.DB) (int, int) {
	t.Helper()
	depts, err := db.ListOrgDepartments(context.Background())
	if err != nil {
		t.Fatalf("读部门失败: %v", err)
	}
	users, err := db.ListOrgUsers(context.Background())
	if err != nil {
		t.Fatalf("读人员失败: %v", err)
	}
	return len(depts), len(users)
}

// TestFetcherPagination 部门列表 has_more 多页遍历 + page_token 透传。
func TestFetcherPagination(t *testing.T) {
	var gotTokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotTokens = append(gotTokens, q.Get("page_token"))
		switch q.Get("page_token") {
		case "":
			writeJSON(t, w, env0(map[string]any{"has_more": true, "page_token": "p2",
				"items": []any{dept("od-a", "A", "0")}}))
		case "p2":
			writeJSON(t, w, env0(map[string]any{"has_more": true, "page_token": "p3",
				"items": []any{dept("od-b", "B", "od-a")}}))
		default:
			writeJSON(t, w, env0(map[string]any{"has_more": false,
				"items": []any{dept("od-c", "C", "od-a")}}))
		}
	}))
	defer srv.Close()
	f := NewFeishuFetcher(TokenSourceFunc(fakeToken), srv.URL, slog.Default())
	depts, err := f.ListDepartments(context.Background())
	if err != nil {
		t.Fatalf("ListDepartments: %v", err)
	}
	if len(depts) != 4 { // 3 页 × 1 条 + 根部门补齐
		t.Fatalf("部门条数 = %d, 期望 4（含根部门）", len(depts))
	}
	if depts[0].OpenDepartmentID != "0" {
		t.Errorf("首条应为根部门 0，实得 %q", depts[0].OpenDepartmentID)
	}
	if gotTokens[1] != "p2" || gotTokens[2] != "p3" {
		t.Errorf("page_token 透传错误: %v", gotTokens)
	}
}

// TestFetcherPaginationNoTokenGuard has_more=true 而无 page_token ⇒ 显式报错（防回环）。
func TestFetcherPaginationNoTokenGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, env0(map[string]any{"has_more": true, "items": []any{}}))
	}))
	defer srv.Close()
	f := NewFeishuFetcher(TokenSourceFunc(fakeToken), srv.URL, slog.Default())
	_, err := f.ListDepartments(context.Background())
	if err == nil || !strings.Contains(err.Error(), "page_token") {
		t.Fatalf("应显式报 page_token 缺失错误，实得 %v", err)
	}
}

// TestRunFull_IdempotentAndSoftDelete 幂等 + 软删 + 复活（口径④）。
func TestRunFull_IdempotentAndSoftDelete(t *testing.T) {
	depts := []map[string]any{dept("od-a", "采购部", "0"), dept("od-b", "生产部", "0")}
	usersByDept := map[string][]map[string]any{
		"0":    {user("ou-root", "根直嘱", []string{"0"})},
		"od-a": {user("ou-1", "张三", []string{"od-a"})},
		"od-b": {user("ou-2", "李四", []string{"od-b"})},
	}
	runner, db, _ := newTestRunner(t, depts, usersByDept, 0, 0)
	ctx := context.Background()

	// ① 第一次全量：3 部门（含根）+ 3 人。
	rep, err := runner.RunNow(ctx)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if rep.DeptTotal != 3 || rep.UserTotal != 3 {
		t.Fatalf("rep = %+v, 期望 dept=3 user=3", rep)
	}
	if rep.DeptAdded != 3 || rep.UserAdded != 3 {
		t.Fatalf("首次应全为新增: %+v", rep)
	}

	// ② 第二次全量（数据不变）：无重复行、无新增。
	rep2, err := runner.RunNow(ctx)
	if err != nil {
		t.Fatalf("第二次 RunNow: %v", err)
	}
	if rep2.DeptAdded != 0 || rep2.UserAdded != 0 {
		t.Fatalf("幂等被破坏: %+v", rep2)
	}
	if dn, un := orgRows(t, db); dn != 3 || un != 3 {
		t.Fatalf("两次后行数 dept=%d user=%d, 期望 3/3（不重复）", dn, un)
	}

	// ③ 远端消失 od-b / ou-2：软删而非物理删。
	usersByDept2 := map[string][]map[string]any{
		"0":    usersByDept["0"],
		"od-a": usersByDept["od-a"],
	}
	runner2, db2, _ := newTestRunner(t, []map[string]any{dept("od-a", "采购部", "0")}, usersByDept2, 0, 0)
	if _, err := runner2.RunNow(ctx); err != nil {
		t.Fatalf("RunNow(差异): %v", err)
	}
	// 用同一库再灌一次全量（构造差异：只给 od-a）。
	if err := db2.UpsertOrgDepartment(ctx, &store.OrgDepartment{OpenDepartmentID: "od-b", Name: "生产部",
		FirstSeenAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Source: "full"}); err != nil {
		t.Fatal(err)
	}
	if err := db2.UpsertOrgUser(ctx, &store.OrgUser{OpenID: "ou-2", Name: "李四",
		PrimaryDepartmentID: "od-b", FirstSeenAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(), Source: "full"}); err != nil {
		t.Fatal(err)
	}
	rep3, err := runner2.RunNow(ctx)
	if err != nil {
		t.Fatalf("差异 RunNow: %v", err)
	}
	if rep3.DeptSoftDeleted != 1 || rep3.UserSoftDeleted != 1 {
		t.Fatalf("应软删 1 部门 1 人: %+v", rep3)
	}
	if dn, un := orgRows(t, db2); dn != 3 || un != 3 {
		t.Fatalf("软删后行数 dept=%d user=%d, 期望 3/3（只增不减）", dn, un)
	}
	var odB store.OrgDepartment
	found := false
	ls, _ := db2.ListOrgDepartments(ctx)
	for _, g := range ls {
		if g.OpenDepartmentID == "od-b" {
			odB, found = g, true
		}
	}
	if !found || !odB.IsDeleted {
		t.Fatalf("od-b 应存在且被软删: %+v found=%v", odB, found)
	}

	// ④ 复活：远端重新出现 od-b/ou-2 ⇒ is_deleted=0。
	runner3, db3, _ := newTestRunner(t, depts, usersByDept, 0, 0)
	if _, err := runner3.RunNow(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db3.UpsertOrgDepartment(ctx, &store.OrgDepartment{OpenDepartmentID: "od-b", Name: "生产部",
		IsDeleted: true, FirstSeenAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(), Source: "full"}); err != nil {
		t.Fatal(err)
	}
	rep4, err := runner3.RunNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep4.DeptSoftDeleted != 0 {
		t.Fatalf("复活后不应再有软删: %+v", rep4)
	}
	ls2, _ := db3.ListOrgDepartments(ctx)
	for _, g := range ls2 {
		if g.OpenDepartmentID == "od-b" && g.IsDeleted {
			t.Fatalf("od-b 应已复活（is_deleted=0）")
		}
	}
}

// TestRunFull_FailureVisible 中途失败 ⇒ 明确错误 + failed 流水 + last_full_error，成功时刻不推进（N5/N6）。
func TestRunFull_FailureVisible(t *testing.T) {
	runner, db, _ := newTestRunner(t,
		[]map[string]any{dept("od-a", "采购部", "0")}, map[string][]map[string]any{}, 40004, 0)
	ctx := context.Background()
	rep, err := runner.RunNow(ctx)
	if err == nil {
		t.Fatalf("应返回错误，实得 %+v", rep)
	}
	if !strings.Contains(err.Error(), "40004") {
		t.Errorf("错误应含平台错误码，实得 %v", err)
	}
	// failed 流水可见。
	var failed int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_org_sync_run WHERE result='failed'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 1 {
		t.Fatalf("t_org_sync_run failed 行数 = %d, 期望 1", failed)
	}
	// last_full_error 可见；成功时刻不推进。
	st, err := db.GetOrgSyncState(ctx)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if st.LastFullError == "" {
		t.Fatal("last_full_error 应非空（不静默）")
	}
	if !st.LastFullSuccessAt.IsZero() {
		t.Fatal("失败不得推进 last_full_success_at（N6）")
	}
	// 用户拉取失败同样可见（部门成功、用户阶段中断）。
	runner2, db2, _ := newTestRunner(t,
		[]map[string]any{dept("od-a", "采购部", "0")},
		map[string][]map[string]any{"od-a": {user("ou-1", "张三", []string{"od-a"})}}, 0, 41050)
	if _, err := runner2.RunNow(ctx); err == nil {
		t.Fatal("用户阶段失败应报错（不得静默半成功）")
	}
	var failed2 int
	if err := db2.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_org_sync_run WHERE result='failed'`).Scan(&failed2); err != nil {
		t.Fatal(err)
	}
	if failed2 != 1 {
		t.Fatalf("用户阶段失败也应留 failed 流水, 实得 %d", failed2)
	}
}

// TestRunFull_EmptyRemoteGuard 远端全空 ⇒ 显式报错、不落库（N6 防御）。
func TestRunFull_EmptyRemoteGuard(t *testing.T) {
	runner, db, _ := newTestRunner(t, []map[string]any{}, map[string][]map[string]any{}, 0, 0)
	if _, err := runner.RunNow(context.Background()); err == nil {
		t.Fatal("空全量应报错")
	}
	if dn, un := orgRows(t, db); dn != 0 || un != 0 {
		t.Fatalf("空全量不得落库, dept=%d user=%d", dn, un)
	}
}

// TestRunFull_FieldGaps 字段权限缺失 ⇒ 缺口计入 field_gaps 且告警可见（N7）。
func TestRunFull_FieldGaps(t *testing.T) {
	depts := []map[string]any{
		{"open_department_id": "od-a", "department_id": "od-a", "parent_department_id": "0"}, // name 缺失
	}
	usersByDept := map[string][]map[string]any{
		"od-a": {user("ou-1", "", []string{"od-a"}), user("ou-2", "李四", nil)}, // 姓名/部门缺失各一
	}
	runner, db, _ := newTestRunner(t, depts, usersByDept, 0, 0)
	rep, err := runner.RunNow(context.Background())
	if err != nil {
		t.Fatalf("字段缺口不应阻断同步: %v", err)
	}
	var gapsJSON string
	if err := db.QueryRowContext(context.Background(),
		`SELECT field_gaps_json FROM t_org_sync_run WHERE id=?`, 1).Scan(&gapsJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gapsJSON, `"dept_name_empty":1`) ||
		!strings.Contains(gapsJSON, `"user_name_empty":1`) ||
		!strings.Contains(gapsJSON, `"user_department_ids_empty":1`) {
		t.Fatalf("field_gaps 缺口计数不符: %s", gapsJSON)
	}
	_ = rep
}

// TestRunFull_ResignedSoftDeleted 离职人员 ⇒ is_deleted=1、行保留、employee_status 归一。
func TestRunFull_ResignedSoftDeleted(t *testing.T) {
	resignedUser := user("ou-quit", "老王", []string{"od-a"},
		"status", map[string]bool{"is_resigned": true, "is_activated": true})
	runner, db, _ := newTestRunner(t,
		[]map[string]any{dept("od-a", "采购部", "0")},
		map[string][]map[string]any{"od-a": {resignedUser}}, 0, 0)
	if _, err := runner.RunNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	us, err := db.ListOrgUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 1 {
		t.Fatalf("离职者应保留 1 行, 实得 %d", len(us))
	}
	if !us[0].IsDeleted || us[0].EmployeeStatus != "离职" {
		t.Fatalf("离职者应软删且状态归一为离职: %+v", us[0])
	}
	// 目录查询不应包含离职者。
	dir, err := db.ListOrgUserDirectory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) != 0 {
		t.Fatalf("离职者不应出现在目录: %+v", dir)
	}
}

// TestRunIfStale 阈值语义：无记录 ⇒ 拉；刚成功 ⇒ 跳过。
func TestRunIfStale(t *testing.T) {
	depts := []map[string]any{dept("od-a", "采购部", "0")}
	usersByDept := map[string][]map[string]any{"od-a": {user("ou-1", "张三", []string{"od-a"})}}
	runner, db, _ := newTestRunner(t, depts, usersByDept, 0, 0)
	ctx := context.Background()

	if err := runner.RunIfStale(ctx); err != nil {
		t.Fatalf("首次 RunIfStale: %v", err)
	}
	var runs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_org_sync_run`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("首次应执行 1 次, 实得 %d", runs)
	}
	if err := runner.RunIfStale(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_org_sync_run`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("新鲜期内应跳过, 实跑次数 %d", runs)
	}
}

// TestPackageBoundary_Reflection 结构性红线（docs/08 §4.10）：orgsync 不得引用
// permission/access/t_user_role 写通道 —— 源码级断言（门禁 grep 的测试内等价物）。
func TestPackageBoundary_Reflection(t *testing.T) {
	// 本测试与包同编译：若 import 了 permission/access，go build 即失败——
	// 此处补一道显式断言，防未来引入带引号字符串式的间接依赖。
	forbidden := []string{"internal/permission", "internal/access", "UpsertUserRole"}
	check := func() error {
		// 只检查本包源文件（fetcher/applier/runner）的**代码行**（跳过 // 注释——
		// 包文档注释里合法提及红线本身）；若 import 了 permission/access，编译期即失败，
		// 此处补一道防「间接引用」的显式断言。
		for _, name := range []string{"fetcher.go", "applier.go", "runner.go"} {
			b, err := readFileSelf(name)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				for _, f := range forbidden {
					if strings.Contains(line, f) {
						return fmt.Errorf("%s 含禁用引用 %q（docs/08 §4.10）", name, f)
					}
				}
			}
		}
		return nil
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
}

func readFileSelf(name string) ([]byte, error) {
	// 测试二进制工作目录为本包目录（go test 约定），直接读源文件。
	return os.ReadFile(name)
}
