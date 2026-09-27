package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestQALedgerDepartmentWriteOnce ★ 探针（8 §4.9-a/b；#64/#73 差集实例）：
// t_ledger_archive.department 是**名称快照**，必须 write-once（保留式）。
//
// 缺陷机制（改前）：upsertArchive 的 DO UPDATE 为
//
//	department = COALESCE(NULLIF(excluded.department,''), t_ledger_archive.department)
//
// 即「新值非空即覆盖」——某人变更部门后，**旧留痕的部门被新名改写**，无异常、无日志（静默）。
// Batch A-5（#46）只改了 instance 侧（repo_instance.go R17 保留式），ledger 侧漏改 ⇒ 修了一半。
//
// ★ 探针边界（必须精确）：两版在**新值为空**时行为相同（都保留）；差异只在**新值为非空的、
// 不同的值**时显现（旧版覆盖 / 新版保留）→ 本探针第二次 upsert 必须用「非空且不同」的值，
// 否则改前改后都绿＝证明不了任何东西。
func TestQALedgerDepartmentWriteOnce(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "qa-ledger-writeonce.db"))
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
			"L02", "PR-2609-0001").Scan(&dept); err != nil {
			t.Fatalf("回读台账失败: %v", err)
		}
		return dept
	}

	// ① 首次落账：department = '技术部'（留痕快照）。
	amt := int64(12345)
	if err := db.UpsertArchive(ctx, db, &LedgerArchive{
		LedgerType: "L02", BizNo: "PR-2609-0001", InstanceCode: "app:PR-2609-0001",
		SourceDocType: "PR", Department: "技术部", AmountCents: &amt,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("首次落账失败: %v", err)
	}
	if got := deptOf(t); got != "技术部" {
		t.Fatalf("首次落账后 department = %q, 期望 技术部（前提不成立）", got)
	}

	// ② 某人变更部门后重放同 (ledger_type, biz_no)：**非空且不同**的新值 '综合运营部'。
	if err := db.UpsertArchive(ctx, db, &LedgerArchive{
		LedgerType: "L02", BizNo: "PR-2609-0001", InstanceCode: "app:PR-2609-0001",
		SourceDocType: "PR", Department: "综合运营部", AmountCents: &amt,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("重放落账失败: %v", err)
	}

	// ③ 快照 write-once：department 必须仍为 '技术部'（不被新名覆盖、不静默改写）。
	if got := deptOf(t); got != "技术部" {
		t.Errorf("★ 缺陷复现：department 快照被后续 upsert 从 技术部 改写为 %q"+
			"（excluded 优先＝新值非空即覆盖，write-once 失守，无异常无日志）", got)
	}
}
