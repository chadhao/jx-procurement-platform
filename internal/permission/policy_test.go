package permission

import (
	"context"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

func seedArchive(t *testing.T, db *store.DB, ledgerType, bizNo, dept, applicant string, extJSON string) {
	t.Helper()
	if err := db.UpsertArchive(context.Background(), db, &store.LedgerArchive{
		LedgerType:      ledgerType,
		BizNo:           bizNo,
		Department:      dept,
		ApplicantOpenID: applicant,
		ExtJSON:         extJSON,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写入台账存档失败: %v", err)
	}
}

// TestRowFilterDeptOnlyOwnDepartment 主管领导（DEPT）只能看到本部门记录（行级过滤，TC-06）。
func TestRowFilterDeptOnlyOwnDepartment(t *testing.T) {
	db := storetest.NewDB(t)
	seedArchive(t, db, "L01", "BA-2609-0001", "生产部", "ou_a", "{}")
	seedArchive(t, db, "L01", "BA-2609-0002", "销售部", "ou_b", "{}")

	idn := Identity{OpenID: "ou_lead", Role: "主管领导", Department: "生产部"}
	cond := RowFilter("a", ScopeDept, idn)
	rows, total, err := db.ListArchive(context.Background(), store.LedgerFilter{
		LedgerType: "L01",
		RowSQL:     cond.SQL,
		RowArgs:    cond.Args,
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("返回行数 = %d (total=%d), 期望仅本部门 1 行", len(rows), total)
	}
	if rows[0].Department != "生产部" {
		t.Errorf("返回了非本部门记录: %s", rows[0].Department)
	}
}

// TestRowFilterSelfBlocksOther 申请人（SELF）直接按 ID 访问他人记录必须被拦截（TC-06）。
func TestRowFilterSelfBlocksOther(t *testing.T) {
	db := storetest.NewDB(t)
	seedArchive(t, db, "L01", "BA-2609-0003", "生产部", "ou_other", "{}")

	idn := Identity{OpenID: "ou_me", Role: "申请人"}
	cond := RowFilter("a", ScopeSelf, idn)

	var n int
	err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_ledger_archive a WHERE a.biz_no = ? AND (`+cond.SQL+`)`,
		append([]any{"BA-2609-0003"}, cond.Args...)...).Scan(&n)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("越权访问未被拦截，命中 %d 行", n)
	}

	// 访问本人记录应放行。
	seedArchive(t, db, "L01", "BA-2609-0004", "生产部", "ou_me", "{}")
	err = db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_ledger_archive a WHERE a.biz_no = ? AND (`+cond.SQL+`)`,
		append([]any{"BA-2609-0004"}, cond.Args...)...).Scan(&n)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("本人记录应可见，命中 %d 行", n)
	}
}

// TestRowFilterDenyAll 无规则（DENY）不返回任何行（deny by default，TC-32）。
func TestRowFilterDenyAll(t *testing.T) {
	cond := RowFilter("a", ScopeDeny, Identity{})
	if cond.SQL != "1=0" {
		t.Errorf("DENY 令牌应生成 1=0，实际 %q", cond.SQL)
	}
}

// TestColumnProjectionDenyAmount 列级投影：采购经办人不得见金额列（连字段名都不出现，TC-07）。
func TestColumnProjectionDenyAmount(t *testing.T) {
	row := map[string]any{
		"biz_no":         "PR-2609-0001",
		"department":     "生产部",
		"amount_cents":   int64(480000),
		"amount_display": "4,800.00",
	}
	deny := []string{"amount_cents", "amount_display"}
	sensitive := map[string]bool{"amount_cents": true, "amount_display": true}

	projected := Project(row, nil, deny, sensitive)
	if _, ok := projected["amount_cents"]; ok {
		t.Error("金额列未被裁剪（越权可见）")
	}
	if _, ok := projected["amount_display"]; ok {
		t.Error("金额展示列未被裁剪")
	}
	if projected["biz_no"] != "PR-2609-0001" {
		t.Error("非敏感列应保留")
	}

	// 项目总经理：显式 allow 金额列，且 deny 为空 → 可见。
	pm := Project(row, []string{"biz_no", "department", "amount_cents", "amount_display"}, nil, sensitive)
	if _, ok := pm["amount_cents"]; !ok {
		t.Error("项目总经理应可见金额列")
	}
}

// TestColumnProjectionDenyOverridesAllow deny 优先于 allow。
func TestColumnProjectionDenyOverridesAllow(t *testing.T) {
	row := map[string]any{"amount_cents": int64(1), "biz_no": "X"}
	out := Project(row, []string{"amount_cents", "biz_no"}, []string{"amount_cents"}, nil)
	if _, ok := out["amount_cents"]; ok {
		t.Error("deny 应优先于 allow")
	}
	if _, ok := out["biz_no"]; !ok {
		t.Error("biz_no 应保留")
	}
}

// TestResolveDenyByDefault 未配置规则的角色 → DENY（不获得任何默认可见数据）。
func TestResolveDenyByDefault(t *testing.T) {
	db := storetest.NewDB(t)
	loader := NewLoader(db)
	rule, err := loader.Resolve(context.Background(), "ledger:L01", Identity{OpenID: "ou_x", Role: "未知角色"})
	if err != nil {
		t.Fatalf("解析规则失败: %v", err)
	}
	if !IsDenyAll(rule) {
		t.Errorf("未配置角色应为 DENY，实际 row_scope=%q", rule.RowScope)
	}
}

// TestResolveFromConfigTable 规则来自配置表（口径变更只改数据，不改代码，ADR-05）。
func TestResolveFromConfigTable(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	if err := db.UpsertPermissionRule(ctx, store.PermissionRule{
		Resource: "ledger:L01", Role: "主管领导", RowScope: "DEPT",
		ColumnDeny: []string{"集团侧人工登记列"},
	}); err != nil {
		t.Fatalf("写入规则失败: %v", err)
	}
	rule, err := NewLoader(db).Resolve(ctx, "ledger:L01", Identity{OpenID: "ou_l", Role: "主管领导"})
	if err != nil {
		t.Fatalf("解析规则失败: %v", err)
	}
	if rule.RowScope != ScopeDept {
		t.Errorf("row_scope = %q, 期望 DEPT", rule.RowScope)
	}
	if len(rule.ColumnDeny) != 1 || rule.ColumnDeny[0] != "集团侧人工登记列" {
		t.Errorf("column_deny 未从配置表装载: %v", rule.ColumnDeny)
	}
}
