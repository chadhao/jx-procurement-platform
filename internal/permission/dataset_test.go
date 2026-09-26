package permission

import (
	"context"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// seedOps 写入一条台账运营行：运营字段 opsJSON 是「指定经办人 / 验收人」的另一处来源。
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

// TestRowFilterAssignedDualSource 指定经办人「双源匹配」回归断言。
//
// 修复前只看存档表 ext_json → 若指派只落在运营表 ops_json，「采购经办人」将看到 0 条记录。
func TestRowFilterAssignedDualSource(t *testing.T) {
	db := storetest.NewDB(t)

	// ① 指派只写运营表（本次修复的核心场景）。
	seedArchive(t, db, "L03", "PR-2609-0001", "生产部", "ou_applicant", "{}")
	seedOps(t, db, "L03", "PR-2609-0001", `{"assigned_open_id":"ou_handler"}`)

	// ② 指派只写存档表（向后兼容，不得退化）。
	seedArchive(t, db, "L03", "PR-2609-0002", "生产部", "ou_applicant", `{"assigned_open_id":"ou_legacy"}`)

	// ③ 两处都没有。
	seedArchive(t, db, "L03", "PR-2609-0003", "生产部", "ou_applicant", "{}")

	cases := []struct {
		name string
		me   string
		want int
	}{
		{"运营表命中", "ou_handler", 1},
		{"存档表命中", "ou_legacy", 1},
		{"两处均无 → 不可放宽", "ou_nobody", 0},
		{"空身份 → 拒绝", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond := RowFilter("a", ScopeAssigned, Identity{OpenID: tc.me})
			if got := visibleRows(t, db, cond); got != tc.want {
				t.Fatalf("可见行数 = %d, 期望 %d（cond=%s args=%v）", got, tc.want, cond.SQL, cond.Args)
			}
		})
	}
}

// TestRowFilterParticipatedDualSource 验收人「双源匹配」回归断言（同 ASSIGNED 一类问题）。
func TestRowFilterParticipatedDualSource(t *testing.T) {
	db := storetest.NewDB(t)

	// ① 验收人只写运营表。
	seedArchive(t, db, "L03", "PR-2609-1001", "生产部", "ou_applicant", "{}")
	seedOps(t, db, "L03", "PR-2609-1001", `{"acceptors":["ou_qc","ou_store"]}`)

	// ② 验收人只写存档表。
	seedArchive(t, db, "L03", "PR-2609-1002", "生产部", "ou_applicant", `{"acceptors":["ou_qc_legacy"]}`)

	// ③ 未参与验收。
	seedArchive(t, db, "L03", "PR-2609-1003", "生产部", "ou_applicant", "{}")

	cases := []struct {
		name string
		me   string
		want int
	}{
		{"运营表命中", "ou_store", 1},
		{"存档表命中", "ou_qc_legacy", 1},
		{"未参与 → 不可放宽", "ou_outsider", 0},
		{"空身份 → 拒绝", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond := RowFilter("a", ScopeParticipated, Identity{OpenID: tc.me})
			if got := visibleRows(t, db, cond); got != tc.want {
				t.Fatalf("可见行数 = %d, 期望 %d（cond=%s args=%v）", got, tc.want, cond.SQL, cond.Args)
			}
		})
	}
}

// TestRowFilterAssignedNotCrossLedgerType 同一 biz_no 跨台账类型不得串号命中。
func TestRowFilterAssignedNotCrossLedgerType(t *testing.T) {
	db := storetest.NewDB(t)
	// 运营行的 ledger_type（L09）与存档行（L03）不同 → EXISTS 子句必须同时比对 ledger_type。
	seedArchive(t, db, "L03", "PR-2609-2001", "生产部", "ou_applicant", "{}")
	seedOps(t, db, "L09", "PR-2609-2001", `{"assigned_open_id":"ou_handler"}`)

	cond := RowFilter("a", ScopeAssigned, Identity{OpenID: "ou_handler"})
	if got := visibleRows(t, db, cond); got != 0 {
		t.Fatalf("跨台账类型串号命中：可见行数 = %d, 期望 0", got)
	}
}

// TestRowFilterForInstancesStillDenies 实例主表无 ext_json / ops，ASSIGNED 与 PARTICIPATED 仍一律拒绝。
func TestRowFilterForInstancesStillDenies(t *testing.T) {
	for _, scope := range []RowScope{ScopeAssigned, ScopeParticipated} {
		cond := RowFilterForInstances(scope, Identity{OpenID: "ou_anyone"})
		if cond.SQL != "1=0" {
			t.Fatalf("scope=%s 在实例主表应拒绝，实际 SQL=%q", scope, cond.SQL)
		}
	}
}

// TestRowFilterParticipatedNoPrefixLeak ★ 越权可见回归：open_id 前缀包含不得造成误命中。
//
// 背景：原实现用 `json_extract(..,'$.acceptors') LIKE '%' || me || '%'`。
// 因 "ou_ab" 是 "ou_abc" 的**前缀**，"ou_ab" 会命中「验收人只有 ou_abc」的记录 → 越权可见。
// 修复：改为对 JSON 数组逐元素**精确比较**（json_each + value = ?）。
func TestRowFilterParticipatedNoPrefixLeak(t *testing.T) {
	db := storetest.NewDB(t)

	// 验收人只有 ou_abc（比 ou_ab 多一个字符）。
	seedArchive(t, db, "L03", "PR-2609-3001", "生产部", "ou_applicant", `{"acceptors":["ou_abc"]}`)
	// 验收人确实含 ou_ab（必须仍可见）。
	seedArchive(t, db, "L03", "PR-2609-3002", "生产部", "ou_applicant", `{"acceptors":["ou_ab","ou_xyz"]}`)
	// 运营表侧同样构造一组前缀关系（双源都要防）。
	seedArchive(t, db, "L03", "PR-2609-3003", "生产部", "ou_applicant", "{}")
	seedOps(t, db, "L03", "PR-2609-3003", `{"acceptors":["ou_abc"]}`)

	cases := []struct {
		name string
		me   string
		want int
	}{
		{"前缀非本人 → 不得命中", "ou_ab", 1}, // 仅 3002 可见，3001/3003 因是 ou_abc 而不可见
		{"本人精确命中（存档表）", "ou_xyz", 1},
		{"本人精确命中（运营表）", "ou_abc", 2}, // 3001（存档）+ 3003（运营）都含 ou_abc
		{"完全无关 → 0", "ou_zzz", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond := RowFilter("a", ScopeParticipated, Identity{OpenID: tc.me})
			if got := visibleRows(t, db, cond); got != tc.want {
				t.Fatalf("可见行数 = %d, 期望 %d（me=%q）——%s", got, tc.want, tc.me,
					"若为 3 说明发生了前缀误命中，行级权限被放大")
			}
		})
	}
}

// TestRowFilterAssignedNoPrefixLeak 指定经办人亦为精确比较（标量等值），前缀关系不得命中。
func TestRowFilterAssignedNoPrefixLeak(t *testing.T) {
	db := storetest.NewDB(t)
	seedArchive(t, db, "L03", "PR-2609-4001", "生产部", "ou_applicant", `{"assigned_open_id":"ou_handler_x"}`)
	seedArchive(t, db, "L03", "PR-2609-4002", "生产部", "ou_applicant", `{"assigned_open_id":"ou_handler"}`)

	if got := visibleRows(t, db, RowFilter("a", ScopeAssigned, Identity{OpenID: "ou_handler"})); got != 1 {
		t.Fatalf("可见行数 = %d, 期望 1（前缀 ou_handler_x 不得被命中）", got)
	}
}

// TestRowFilterMalformedJSONDoesNotBreakQuery ★ 脏数据不得炸掉整条查询。
//
// 背景：SQLite 的 `json_extract` / `json_each` 遇非法 JSON 会抛
// `SQL logic error: malformed JSON` —— 这是**整条查询失败**，即 `ext_json` 里
// 只要有一行被写脏，台账列表 / 看板 / 实例查询就全部 500。
// 修复：外层套 `CASE WHEN json_valid(col)` 守卫（用 CASE 而非 AND，因 AND 不保证求值顺序）。
func TestRowFilterMalformedJSONDoesNotBreakQuery(t *testing.T) {
	db := storetest.NewDB(t)

	seedArchive(t, db, "L03", "PR-2609-5001", "生产部", "ou_applicant", `{"acceptors":["ou_qc"]}`)
	// 真正非法的 JSON（注意：**空串不是**脏数据 —— store.UpsertArchive 用 defaultStr 把它规范化为 "{}"）。
	seedArchive(t, db, "L03", "PR-2609-5002", "生产部", "ou_applicant", `{"acceptors": 这行不是合法 JSON}`)
	// 合法 JSON 但类型不符（acceptors 是数字而非数组/字符串）。
	seedArchive(t, db, "L03", "PR-2609-5003", "生产部", "ou_applicant", `{"acceptors": 12345}`)
	// 合法 JSON 但键缺失。
	seedArchive(t, db, "L03", "PR-2609-5005", "生产部", "ou_applicant", `{"other":"x"}`)
	// 运营表侧脏数据。
	seedOps(t, db, "L03", "PR-2609-5004", `{"acceptors": 也不合法}`)
	seedArchive(t, db, "L03", "PR-2609-5004", "生产部", "ou_applicant", "{}")

	// ① 合法行仍可被正常命中，且查询**不报错**（visibleRows 内部 Fatalf 即失败）。
	if got := visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "ou_qc"})); got != 1 {
		t.Fatalf("脏数据存在时可见行数 = %d, 期望 1（合法行应仍可见）", got)
	}
	// ② 对「任何人」都不因脏数据报错，且不会把脏行放行。
	if got := visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "ou_anyone"})); got != 0 {
		t.Fatalf("脏行被放行：可见行数 = %d, 期望 0（非法 JSON 必须判为不匹配）", got)
	}
	if got := visibleRows(t, db, RowFilter("a", ScopeParticipated, Identity{OpenID: "12345"})); got != 0 {
		t.Fatalf("类型不符的 acceptors 被匹配：可见行数 = %d, 期望 0（数字 12345 不得等于字符串 \"12345\"）", got)
	}
	// ③ ASSIGNED 侧同样不得因脏数据报错。
	if got := visibleRows(t, db, RowFilter("a", ScopeAssigned, Identity{OpenID: "ou_anyone"})); got != 0 {
		t.Fatalf("ASSIGNED 脏数据放行：可见行数 = %d, 期望 0", got)
	}
	// ④ 未命中任何行时也不报错。
	if got := visibleRows(t, db, RowFilter("a", ScopeAssigned, Identity{OpenID: "ou_qc"})); got != 0 {
		t.Fatalf("可见行数 = %d, 期望 0", got)
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

// TestRowFilterForSubmissionScoping ★ 报送表行过滤：只有 ALL 才拿到全量。
//
// 修复前，异常面板的「集团驳回后未处置」是**全表 COUNT(*)**，而默认种子把 `dashboard:4`
// 发给了全部 10 个角色（含申请人 SELF / 采购经办人 ASSIGNED / 验收人 PARTICIPATED）→
// 受限角色可读到自己无权查看的全局聚合值。
//
// ★ Q14-B 第 5 项（2026-09-26 决定「按真实列重建」）：migrations/0003 为 t_submission 补上了
// `department` / `applicant_open_id` / `assigned_open_id` / `acceptors` 四列，故本用例已从
// 「受限 scope 一律降级为 created_by = me」改为**按真实列断言各令牌的真实语义**。
func TestRowFilterForSubmissionScoping(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()

	// 三条报送，身份字段各不相同：
	//   A：申请人 ou_app_a，部门 生产部，经办人 ou_h_a，验收人 [ou_acc_a]
	//   B：申请人 ou_app_b，部门 销售部，经办人 ou_h_b，验收人 [ou_acc_b]
	//   C：申请人 ou_app_a，部门 生产部，无经办人、无验收人
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
		// SELF 按申请人列（不再按登记人）
		{"SELF = 本人为申请人 → 2 条", ScopeSelf, Identity{OpenID: "ou_app_a"}, 2},
		{"SELF 无本人申请 → 0", ScopeSelf, Identity{OpenID: "ou_ops_a"}, 0},
		// DEPT 按 department 列
		{"DEPT 生产部 → 2 条", ScopeDept, Identity{OpenID: "ou_x", Department: "生产部"}, 2},
		{"DEPT 销售部 → 1 条", ScopeDept, Identity{OpenID: "ou_x", Department: "销售部"}, 1},
		{"DEPT 无部门 → 0", ScopeDept, Identity{OpenID: "ou_x"}, 0},
		// CHARGE_DEPT 只看分管部门（ExtraDepts），不看本人部门
		{"CHARGE_DEPT 分管销售部 → 1 条", ScopeChargeDept, Identity{OpenID: "ou_x", Department: "生产部", ExtraDepts: []string{"销售部"}}, 1},
		{"CHARGE_DEPT 无分管 → 0", ScopeChargeDept, Identity{OpenID: "ou_x", Department: "生产部"}, 0},
		// ASSIGNED 按 assigned_open_id 列
		{"ASSIGNED 命中经办人 → 1 条", ScopeAssigned, Identity{OpenID: "ou_h_a"}, 1},
		{"ASSIGNED 非经办人 → 0", ScopeAssigned, Identity{OpenID: "ou_ops_a"}, 0},
		// PARTICIPATED 按 acceptors 列
		{"PARTICIPATED 命中验收人 → 1 条", ScopeParticipated, Identity{OpenID: "ou_acc_b"}, 1},
		{"PARTICIPATED 非验收人 → 0", ScopeParticipated, Identity{OpenID: "ou_h_a"}, 0},
		// ★ 前缀包含关系不得越权（open_id 子串匹配是历史高危点）
		{"ASSIGNED 前缀不同不得命中", ScopeAssigned, Identity{OpenID: "ou_h"}, 0},
		{"PARTICIPATED 前缀不同不得命中", ScopeParticipated, Identity{OpenID: "ou_acc"}, 0},
		// fail-closed
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
