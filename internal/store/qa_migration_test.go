package store

import (
	"context"
	"path/filepath"
	"testing"
)

// 本文件为 QA 独立验证（新增，不修改既有文件）：迁移与 NULL/唯一约束边界。
//
// ★ 为什么重要：本系统大量依赖「唯一约束」做幂等兜底；而 SQLite 的**列级 UNIQUE 对 NULL 不生效**
// （可插入任意多条 NULL）。若 0002 的表达式唯一索引缺失，业务单号为空时去重会形同虚设——
// 且不会报错（静默重复）。本文件用**确定性**断言检测该防线是否存在。

func qaColumns(t *testing.T, db *DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) 失败: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("扫描表结构失败: %v", err)
		}
		out[name] = true
	}
	return out
}

// TestQAMigrationFreshAndIdempotent 全新库依次跑全部迁移（0001→…→最新）；t_submission 含 0003 的 4 个新列、
// t_ledger_archive 含 0004 的 supplier_norm；
// 重复执行 Migrate 不得报错。
func TestQAMigrationFreshAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "qa-migrate.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	// t_attachment 必须存在（0005，B39 附件元数据）。
	if n := qaColumns(t, db, "t_attachment"); len(n) == 0 {
		t.Errorf("t_attachment 表不存在（migrations/0005 未生效）")
	} else if !n["file_id"] || !n["instance_code"] || !n["storage_key"] {
		t.Errorf("t_attachment 关键列缺失: %v", n)
	}

	// t_ledger_archive 必须含 0004 补上的 supplier_norm（Q20 分组键）。
	if c := qaColumns(t, db, "t_ledger_archive"); !c["supplier_norm"] {
		t.Errorf("t_ledger_archive 缺列 supplier_norm（migrations/0004 未生效）")
	}

	// t_submission 必须含 0003 补上的 4 个真实列。
	cols := qaColumns(t, db, "t_submission")
	for _, c := range []string{"department", "applicant_open_id", "assigned_open_id", "acceptors"} {
		if !cols[c] {
			t.Errorf("t_submission 缺列 %s（migrations/0003 未生效）", c)
		}
	}
	// 三个迁移版本均已登记。
	// ★ 迁移版本清单随新增迁移同步（0004 为 Q20 供应商归一列）。
	for _, v := range []string{"0001_init.sql", "0002_idem_unique.sql", "0003_submission_scope.sql",
		"0004_supplier_norm.sql", "0005_attachment.sql"} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM t_schema_migrations WHERE version = ?`, v).Scan(&n); err != nil {
			t.Fatalf("查版本表失败: %v", err)
		}
		if n != 1 {
			t.Errorf("迁移版本 %s 登记数 = %d, 期望 1", v, n)
		}
	}

	// ★ 重复执行必须幂等（不得因「CREATE TABLE/ALTER TABLE 已存在」报错）。
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("重复迁移应幂等无错，实际: %v", err)
	}
	// 两次迁移后版本表不得出现重复。
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_schema_migrations`).Scan(&total); err != nil {
		t.Fatalf("查版本总数失败: %v", err)
	}
	// ★ 期望值＝仓库内迁移文件数（新增迁移时同步此处；0004 Q20 supplier_norm、0005 附件元数据）。
	const wantMigrations = 5
	if total != wantMigrations {
		t.Errorf("迁移版本总数 = %d, 期望 %d（重复执行不得重复登记）", total, wantMigrations)
	}
}

// TestQASubmissionNullBizNoHardStop 验证「SQLite 列级 UNIQUE 对 NULL 不生效」这一事实，以及
// 0002 表达式唯一索引带来的**硬兜底**：biz_no 为空至多允许一条。
func TestQASubmissionNullBizNoHardStop(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "qa-null.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	insert := `INSERT INTO t_submission(biz_no, submit_state, created_at, updated_at)
	           VALUES (NULL, '未提交', '2026-09-26T00:00:00Z', '2026-09-26T00:00:00Z')`
	if _, err := db.ExecContext(ctx, insert); err != nil {
		t.Fatalf("首条 NULL 单号插入失败: %v", err)
	}
	// 第二条 NULL 单号：列级 UNIQUE 不会拦（NULL 互不相等），必须由 COALESCE 表达式索引拦下。
	if _, err := db.ExecContext(ctx, insert); err == nil {
		t.Errorf("第二条 NULL 业务单号插入成功 → ux_submission_biz_no_nonnull 未生效，去重形同虚设")
	}

	// 非空重复单号：列级 UNIQUE + 表达式索引双重兜底，必须被拒。
	nonNull := `INSERT INTO t_submission(biz_no, submit_state, created_at, updated_at)
	            VALUES ('SUB-QA-DUP', '未提交', '2026-09-26T00:00:00Z', '2026-09-26T00:00:00Z')`
	if _, err := db.ExecContext(ctx, nonNull); err != nil {
		t.Fatalf("首条非空单号插入失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, nonNull); err == nil {
		t.Errorf("重复非空业务单号插入成功 → 业务唯一键失效")
	}

	// 确认两个索引确实存在。
	for _, name := range []string{"ux_audit_idem_key", "ux_submission_biz_no_nonnull"} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = ?`, name).Scan(&n); err != nil {
			t.Fatalf("查索引失败: %v", err)
		}
		if n != 1 {
			t.Errorf("索引 %s 不存在", name)
		}
	}
}
