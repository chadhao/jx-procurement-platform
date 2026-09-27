package store

// qa_verify_final_ledger_test.go —— 独立验证者（qa-verify-final）验证测试**收编入库**版。
//
// 来源与依据：首轮独立验证（qa-verify-final，HEAD=25066a0）对 cc753c4（R17 同形·write-once，
// 8 §4.9-a/b；#46 只改 instance 侧、ledger 侧漏改）的独立探针，绿→红双向实证后收编守门。

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestQAVFLedgerDepartmentWriteOnce —— 独立验证者（qa-verify-final）对 cc753c4 的自建探针。
//
// ★ 语义边界（精确）：两版 SQL 在「新值为空」时行为相同（都保留）；差异只在
// 「新值为非空且不同」时显现（改前＝excluded 优先即覆盖；改后＝保留式）。
// 故本探针第二次 upsert 必须用「非空且不同」的值，否则改前改后都绿＝证明不了任何东西。
// （与作者探针 TestQALedgerDepartmentWriteOnce 的取值/单号刻意不同构，互为冗余备份。）
func TestQAVFLedgerDepartmentWriteOnce(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "qa-vf-ledger-writeonce.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	now := time.Now()

	deptOf := func(t *testing.T) string {
		t.Helper()
		var dept string
		if err := db.QueryRowContext(ctx,
			`SELECT COALESCE(department,'') FROM t_ledger_archive WHERE ledger_type=? AND biz_no=?`,
			"L01", "PR-QA-777").Scan(&dept); err != nil {
			t.Fatalf("回读台账失败: %v", err)
		}
		return dept
	}

	// ① 首次落账：department = '研发中心'（历史快照）。
	amt := int64(8800)
	if err := db.UpsertArchive(ctx, db, &LedgerArchive{
		LedgerType: "L01", BizNo: "PR-QA-777", InstanceCode: "app:PR-QA-777",
		SourceDocType: "PR", Department: "研发中心", AmountCents: &amt,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("首次落账失败: %v", err)
	}
	if got := deptOf(t); got != "研发中心" {
		t.Fatalf("前提不成立：首次落账后 department = %q, 期望 研发中心", got)
	}

	// ② 某人变更部门后以「非空且不同」的新值 '财务部' 重放同一 (ledger_type, biz_no)。
	if err := db.UpsertArchive(ctx, db, &LedgerArchive{
		LedgerType: "L01", BizNo: "PR-QA-777", InstanceCode: "app:PR-QA-777",
		SourceDocType: "PR", Department: "财务部", AmountCents: &amt,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("重放落账失败: %v", err)
	}
	// 核心断言：快照 write-once —— 仍为 '研发中心'，不得被非空新值覆盖。
	if got := deptOf(t); got != "研发中心" {
		t.Errorf("★ 失守：department 快照被后续 upsert 从 研发中心 改写为 %q（write-once 失效）", got)
	}

	// ③ 边界（两版同形的公共行为）：新值为空 → 保留既有值。
	if err := db.UpsertArchive(ctx, db, &LedgerArchive{
		LedgerType: "L01", BizNo: "PR-QA-777", InstanceCode: "app:PR-QA-777",
		SourceDocType: "PR", Department: "", AmountCents: &amt,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("空值重放落账失败: %v", err)
	}
	if got := deptOf(t); got != "研发中心" {
		t.Errorf("空新值不得清空既有快照，实际 %q", got)
	}
}
