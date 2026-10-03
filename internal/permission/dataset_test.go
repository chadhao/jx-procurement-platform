package permission

// N-042 权限位点验收（docs/19 §4 七判据，全部双向）：
//	① ASSIGNED 正向命中；② 反向不越权（防改成恒真）；③ PARTICIPATED 的 JSON 数组列专测；
//	④ 前缀越权专测（ou_ab 不得命中 ou_abc）；⑤ 脏 JSON fail-closed 且查询不报错；
//	⑥ t_ledger_archive 与 t_instance 对称；⑦ 写入端专测在 flow/httpapi 侧（防「有列无数据」）。
//
// ★ 消费端语义（docs/19 §3.3）：ASSIGNED → designated_open_id = ?（精确等值，绝不用 LIKE）；
//	PARTICIPATED → textOrJSONContains("acceptors")（JSON 数组或普通文本均精确匹配）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// seedScope 写入一条带权限位点规范列的台账行（0019 新列）。
func seedScope(t *testing.T, db *store.DB, ledgerType, bizNo, designated, acceptors string) {
	t.Helper()
	if err := db.UpsertArchive(context.Background(), db, &store.LedgerArchive{
		LedgerType:       ledgerType,
		BizNo:            bizNo,
		Department:       "生产部",
		ApplicantOpenID:  "ou_applicant",
		DesignatedOpenID: designated,
		Acceptors:        acceptors,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写入台账存档失败(%s/%s): %v", ledgerType, bizNo, err)
	}
}

// seedOps 写入一条台账运营行（保留：旧双源场景退役后仅作干扰数据，证明过滤不再依赖它）。
func seedOps(t *testing.T, db *store.DB, ledgerType, bizNo, opsJSON string) {
	t.Helper()
	if err := db.UpsertOps(context.Background(), &store.LedgerOps{
		LedgerType: ledgerType, BizNo: bizNo, OpsJSON: opsJSON, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写入台账运营行失败(%s/%s): %v", ledgerType, bizNo, err)
	}
}

// visibleRows 走仓储查询统计某行过滤条件下的可见行数（行过滤必须发生在 SQL 层）。
func visibleRows(t *testing.T, db *store.DB, cond Condition) int {
	t.Helper()
	_, total, err := db.ListArchive(context.Background(), store.LedgerFilter{
		LedgerType: "L03",
		RowSQL:     cond.SQL,
		RowArgs:    cond.Args,
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	return total
}

// visibleInstances 走 t_instance 直查（判据⑥两表对称的 instance 侧）。
func visibleInstances(t *testing.T, db *store.DB, cond Condition) int {
	t.Helper()
	where := "1=1"
	args := []any{}
	if cond.SQL != "" {
		where += " AND (" + cond.SQL + ")"
		args = append(args, cond.Args...)
	}
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM t_instance WHERE "+where, args...).Scan(&n); err != nil {
		t.Fatalf("实例查询失败（SQL 不得报错 —— 判据⑤）: %v", err)
	}
	return n
}

// TestRowFilterAssignedOnSpecColumn 判据①②：ASSIGNED 按规范列精确命中 + 反向不越权。
func TestRowFilterAssignedOnSpecColumn(t *testing.T) {
	db := storetest.NewDB(t)
	seedScope(t, db, "L03", "PR-2609-0001", "ou_handler", "")                  // 指定经办
	seedScope(t, db, "L03", "PR-2609-0002", "", "")                            // 未指定
	seedOps(t, db, "L03", "PR-2609-0002", `{"assigned_open_id":"ou_handler"}`) // 干扰：ops 有值但列没有 ⇒ 不命中（新语义只认规范列）

	cases := []struct {
		name string
		me   string
		want int
	}{
		{"① 正向命中", "ou_handler", 1},
		{"② 反向不越权（他人看不见）", "ou_other", 0},
		{"干扰：仅运营表有值不命中", "ou_ops_only", 0},
		{"空身份 fail-closed", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond := RowFilter("a", ScopeAssigned, Identity{OpenID: tc.me})
			if got := visibleRows(t, db, cond); got != tc.want {
				t.Fatalf("可见行数 = %d, 期望 %d（SQL=%s args=%v）", got, tc.want, cond.SQL, cond.Args)
			}
		})
	}
}

// TestRowFilterParticipatedJSONArray 判据③：PARTICIPATED 的 JSON 数组列专测。
func TestRowFilterParticipatedJSONArray(t *testing.T) {
	db := storetest.NewDB(t)
	seedScope(t, db, "L03", "PR-2609-1001", "", `["ou_qc","ou_store"]`)
	seedScope(t, db, "L03", "PR-2609-1002", "", "")
	// 普通文本形态（非 JSON）也要能等值命中（textOrJSONContains 的兼容面）
	seedScope(t, db, "L03", "PR-2609-1003", "", "ou_plain")

	cases := []struct {
		name string
		me   string
		want int
	}{
		{"数组成员命中", "ou_store", 1},
		{"数组外人员不越权", "ou_outsider", 0},
		{"普通文本列精确命中", "ou_plain", 1},
		{"空身份拒绝", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond := RowFilter("a", ScopeParticipated, Identity{OpenID: tc.me})
			if got := visibleRows(t, db, cond); got != tc.want {
				t.Fatalf("可见行数 = %d, 期望 %d（SQL=%s args=%v）", got, tc.want, cond.SQL, cond.Args)
			}
		})
	}
}

// TestRowFilterNoPrefixLeak 判据④：前缀越权专测 —— ou_ab 不得命中 ou_abc 的记录。
func TestRowFilterNoPrefixLeak(t *testing.T) {
	db := storetest.NewDB(t)
	seedScope(t, db, "L03", "PR-2609-3001", "ou_abc", "")     // designated = ou_abc
	seedScope(t, db, "L03", "PR-2609-3002", "", `["ou_abc"]`) // acceptors = [ou_abc]
	seedScope(t, db, "L03", "PR-2609-3003", "ou_handler_x", `["ou_handler_x"]`)

	// ASSIGNED：前缀 ou_handler 不得命中 ou_handler_x
	if got := visibleRows(t, db, RowFilter("a", ScopeAssigned, Identity{OpenID: "ou_handler"})); got != 0 {
		t.Errorf("ASSIGNED 前缀越权：可见 = %d, 期望 0", got)
	}
	// PARTICIPATED：前缀 ou_ab 不得命中 ou_abc；ou_abc 精确命中 1
	if got := visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "ou_ab"})); got != 0 {
		t.Errorf("PARTICIPATED 前缀越权：可见 = %d, 期望 0", got)
	}
	if got := visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "ou_abc"})); got != 1 {
		t.Errorf("精确命中 = %d, 期望 1", got)
	}
}

// TestRowFilterDirtyColumnFailClosed 判据⑤：脏列（非法 JSON 文本）fail-closed 且查询不报错。
func TestRowFilterDirtyColumnFailClosed(t *testing.T) {
	db := storetest.NewDB(t)
	seedScope(t, db, "L03", "PR-2609-5001", "", `["ou_qc"]`)  // 合法行
	seedScope(t, db, "L03", "PR-2609-5002", "", "{不是合法 JSON") // 脏列
	seedScope(t, db, "L03", "PR-2609-5003", "", "")           // 空列

	// 合法行仍命中；脏行不放行；查询不报错（visibleRows 内部 Fatalf 即失败）
	if got := visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "ou_qc"})); got != 1 {
		t.Fatalf("合法行可见 = %d, 期望 1", got)
	}
	// 脏文本整段等值：与任何 ou_* 不同 ⇒ 不放行
	if got := visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "ou_anyone"})); got != 0 {
		t.Fatalf("脏行被放行：可见 = %d, 期望 0", got)
	}
	// ASSIGNED 侧同样不因脏/空报错
	if got := visibleRows(t, db, RowFilter("a", ScopeAssigned, Identity{OpenID: "ou_anyone"})); got != 0 {
		t.Fatalf("ASSIGNED 可见 = %d, 期望 0", got)
	}
}

// TestScopeSymmetricBothTables 判据⑥：t_instance 与 t_ledger_archive 对称 ——
// 同一身份在两表上按同款谓词得到同样的命中/不命中。
func TestScopeSymmetricBothTables(t *testing.T) {
	db := storetest.NewDB(t)
	// 台账行 + 实例行（designated / acceptors 各一条）
	seedScope(t, db, "L03", "PR-2609-6001", "ou_sym", `["ou_acc"]`)
	seedScope(t, db, "L03", "PR-2609-6002", "", "")
	if err := db.UpsertInstance(context.Background(), &store.Instance{
		InstanceCode: "I-PR-2609-6001", ApprovalCode: "code-x", DocType: "PR",
		BizNo: "PR-2609-6001", Status: "PENDING", ApplicantOpenID: "ou_applicant",
		Department: "生产部", DesignatedOpenID: "ou_sym", Acceptors: `["ou_acc"]`,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInstance(context.Background(), &store.Instance{
		InstanceCode: "I-PR-2609-6002", ApprovalCode: "code-x", DocType: "PR",
		BizNo: "PR-2609-6002", Status: "PENDING", ApplicantOpenID: "ou_applicant",
		Department: "生产部",
		CreatedAt:  time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// ASSIGNED：两表都命中 1 / 不命中 0
	arch := visibleRows(t, db, RowFilter("a", ScopeAssigned, Identity{OpenID: "ou_sym"}))
	inst := visibleInstances(t, db, RowFilterForInstances(ScopeAssigned, Identity{OpenID: "ou_sym"}))
	if arch != 1 || inst != 1 {
		t.Errorf("ASSIGNED 对称失败：archive=%d instance=%d, 期望 1/1", arch, inst)
	}
	arch = visibleRows(t, db, RowFilter("a", ScopeAssigned, Identity{OpenID: "ou_nope"}))
	inst = visibleInstances(t, db, RowFilterForInstances(ScopeAssigned, Identity{OpenID: "ou_nope"}))
	if arch != 0 || inst != 0 {
		t.Errorf("ASSIGNED 反向对称失败：archive=%d instance=%d, 期望 0/0", arch, inst)
	}
	// PARTICIPATED：同样对称
	arch = visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "ou_acc"}))
	inst = visibleInstances(t, db, RowFilterForInstances(ScopeParticipated, Identity{OpenID: "ou_acc"}))
	if arch != 1 || inst != 1 {
		t.Errorf("PARTICIPATED 对称失败：archive=%d instance=%d, 期望 1/1", arch, inst)
	}
	arch = visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "ou_nope"}))
	inst = visibleInstances(t, db, RowFilterForInstances(ScopeParticipated, Identity{OpenID: "ou_nope"}))
	if arch != 0 || inst != 0 {
		t.Errorf("PARTICIPATED 反向对称失败：archive=%d instance=%d, 期望 0/0", arch, inst)
	}
}

// TestRowFilterForInstancesScope 实例侧：ASSIGNED/PARTICIPATED 不再 1=0（0019 后）。
func TestRowFilterForInstancesScope(t *testing.T) {
	for _, scope := range []RowScope{ScopeAssigned, ScopeParticipated} {
		cond := RowFilterForInstances(scope, Identity{OpenID: "ou_anyone"})
		if cond.SQL == "1=0" {
			t.Fatalf("scope=%s 在 0019 后不应恒 1=0（规则写了数据承载没有）", scope)
		}
		if len(cond.Args) == 0 {
			t.Fatalf("scope=%s 缺身份参数", scope)
		}
	}
	// 空身份仍 fail-closed（不是「改成恒真」）
	for _, scope := range []RowScope{ScopeAssigned, ScopeParticipated} {
		cond := RowFilterForInstances(scope, Identity{OpenID: ""})
		if cond.SQL != "1=0" {
			t.Fatalf("空身份 scope=%s 应 1=0, 实际 %q", scope, cond.SQL)
		}
	}
	// 普通 scope 走 RowFilter 透传（SELF 等语义不变）
	cond := RowFilterForInstances(ScopeSelf, Identity{OpenID: "ou_me"})
	if !strings.Contains(cond.SQL, "applicant_open_id") {
		t.Errorf("SELF 应按申请人列: %q", cond.SQL)
	}
}

// TestRowFilterDenyAndEmptyFailClosed 兜底：未知 scope 与空身份一律拒绝（fail-closed）。
func TestRowFilterDenyAndEmptyFailClosed(t *testing.T) {
	conds := []Condition{
		RowFilter("a", ScopeDeny, Identity{OpenID: "ou_x"}),
		RowFilter("a", RowScope("NO_SUCH_SCOPE"), Identity{OpenID: "ou_x"}),
		RowFilter("a", ScopeSelf, Identity{}),
		RowFilter("a", ScopeDept, Identity{}),
		RowFilter("a", ScopeChargeDept, Identity{}),
	}
	for i, c := range conds {
		if c.SQL != "1=0" {
			t.Errorf("第 %d 个条件应为拒绝，实际 SQL=%q", i, c.SQL)
		}
	}
}

// TestRowFilterForSubmissionScoping 报送表行过滤（0003 先例 —— 本件的同款修复）：
// 只有 ALL 拿全量，各令牌按真实列断言语义。
func TestRowFilterForSubmissionScoping(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()

	rows := []*store.Submission{
		{BizNo: "SUB-SCOPE-A", SubjectType: "公户付款", SubmitState: "未提交", GrpState: "已驳回",
			Department: "生产部", ApplicantOpenID: "ou_app_a", AssignedOpenID: "ou_h_a",
			Acceptors: `["ou_acc_a"]`, CreatedBy: "ou_ops_a"},
		{BizNo: "SUB-SCOPE-B", SubjectType: "公户付款", SubmitState: "未提交", GrpState: "已驳回",
			Department: "销售部", ApplicantOpenID: "ou_app_b", AssignedOpenID: "ou_h_b",
			Acceptors: `["ou_acc_b"]`, CreatedBy: "ou_ops_b"},
		{BizNo: "SUB-SCOPE-C", SubjectType: "公户付款", SubmitState: "未提交", GrpState: "已驳回",
			Department: "生产部", ApplicantOpenID: "ou_app_a", CreatedBy: "ou_ops_a"},
	}
	for _, row := range rows {
		if _, err := db.CreateSubmission(ctx, row); err != nil {
			t.Fatalf("写入报送失败: %v", err)
		}
	}

	countWith := func(cond Condition) int {
		t.Helper()
		where := `COALESCE(grp_state,'') LIKE '%驳回%' AND COALESCE(reject_reason,'') = ''`
		args := []any{}
		if cond.SQL != "" {
			where += " AND (" + cond.SQL + ")"
			args = append(args, cond.Args...)
		}
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM t_submission s WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatalf("按行过滤统计失败（SQL=%q）: %v", cond.SQL, err)
		}
		return n
	}

	cases := []struct {
		name  string
		scope RowScope
		id    Identity
		want  int
	}{
		{"ALL 拿到全量", ScopeAll, Identity{OpenID: "ou_anyone"}, 3},
		{"SELF = 本人为申请人 → 2 条", ScopeSelf, Identity{OpenID: "ou_app_a"}, 2},
		{"SELF 无本人申请 → 0", ScopeSelf, Identity{OpenID: "ou_ops_a"}, 0},
		{"DEPT 生产部 → 2 条", ScopeDept, Identity{OpenID: "ou_x", Department: "生产部"}, 2},
		{"DEPT 销售部 → 1 条", ScopeDept, Identity{OpenID: "ou_x", Department: "销售部"}, 1},
		{"DEPT 无部门 → 0", ScopeDept, Identity{OpenID: "ou_x"}, 0},
		{"CHARGE_DEPT 分管销售部 → 1 条", ScopeChargeDept, Identity{OpenID: "ou_x", Department: "生产部", ExtraDepts: []string{"销售部"}}, 1},
		{"CHARGE_DEPT 无分管 → 0", ScopeChargeDept, Identity{OpenID: "ou_x", Department: "生产部"}, 0},
		{"ASSIGNED 命中经办人 → 1 条", ScopeAssigned, Identity{OpenID: "ou_h_a"}, 1},
		{"ASSIGNED 非经办人 → 0", ScopeAssigned, Identity{OpenID: "ou_ops_a"}, 0},
		{"PARTICIPATED 命中验收人 → 1 条", ScopeParticipated, Identity{OpenID: "ou_acc_b"}, 1},
		{"PARTICIPATED 非验收人 → 0", ScopeParticipated, Identity{OpenID: "ou_h_a"}, 0},
		{"ASSIGNED 前缀不同不得命中", ScopeAssigned, Identity{OpenID: "ou_h"}, 0},
		{"PARTICIPATED 前缀不同不得命中", ScopeParticipated, Identity{OpenID: "ou_acc"}, 0},
		{"DENY → 0", ScopeDeny, Identity{OpenID: "ou_app_a"}, 0},
		{"未知 scope → 0", RowScope("NOPE"), Identity{OpenID: "ou_app_a"}, 0},
		{"空身份 SELF → 0", ScopeSelf, Identity{OpenID: ""}, 0},
		{"空身份 ASSIGNED → 0", ScopeAssigned, Identity{OpenID: ""}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond := RowFilterForSubmission(tc.scope, tc.id, "s")
			if got := countWith(cond); got != tc.want {
				t.Fatalf("可见/计入条数 = %d, 期望 %d（scope=%s me=%q SQL=%q 参数=%v）",
					got, tc.want, tc.scope, tc.id.OpenID, cond.SQL, cond.Args)
			}
		})
	}
}
