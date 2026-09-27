package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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
	// 架构转向 ③ 地基层（migration 0007）新增 6 表 + t_instance 增列，必须生效。
	for _, tb := range []string{"t_doc_seq", "t_flow_task", "t_flow_op_log",
		"t_approval_def", "t_push_record", "t_notify_log"} {
		if n := qaColumns(t, db, tb); len(n) == 0 {
			t.Errorf("表 %s 不存在（migrations/0007 未生效）", tb)
		}
	}
	instCols := qaColumns(t, db, "t_instance")
	for _, c := range []string{"update_time", "prev_biz_no", "cancel_reason", "cancel_at", "push_hash", "push_at"} {
		if !instCols[c] {
			t.Errorf("t_instance 缺列 %s（migrations/0007 未生效）", c)
		}
	}
	// 架构转向 ③ 顺序会签（migration 0008/0009）：t_flow_task 补 release_state / weight / task_order。
	ftCols := qaColumns(t, db, "t_flow_task")
	for _, c := range []string{"release_state", "weight", "task_order"} {
		if !ftCols[c] {
			t.Errorf("t_flow_task 缺列 %s（migrations/0008、0009 未生效）", c)
		}
	}
	// 架构转向 ③（migration 0010）：t_instance 补 ext_json（非规范字段）。
	if !instCols["ext_json"] {
		t.Errorf("t_instance 缺列 ext_json（migrations/0010 未生效）")
	}
	// release_state 必须有默认值（NOT NULL DEFAULT 'HELD'）—— 新行漏写时不得为 NULL。
	var dflt sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT dflt_value FROM pragma_table_info('t_flow_task') WHERE name='release_state'`).Scan(&dflt); err != nil {
		t.Fatalf("查 release_state 默认值失败: %v", err)
	}
	if !dflt.Valid || strings.Trim(dflt.String, "'\"") != "HELD" {
		t.Errorf("t_flow_task.release_state 默认值 = %v, 期望 'HELD'", dflt)
	}
	// ★ 前提 P3：UNIQUE(biz_no) 兜底必须存在（锁号不被静默破坏）。
	var nBizUx int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='ux_instance_biz_no'`).Scan(&nBizUx); err != nil {
		t.Fatalf("查索引失败: %v", err)
	}
	if nBizUx != 1 {
		t.Errorf("唯一索引 ux_instance_biz_no 不存在（锁号前提 P3 被破坏）")
	}

	// 迁移版本清单随新增迁移同步（0004 供应商归一、0005 附件、0006 台账一对多、0007 审批核心、
	// 0008 顺序会签释放、0009 task_order 次序契约、0010 t_instance.ext_json、
	// 0011 回调幂等键入 round）。
	for _, v := range []string{"0001_init.sql", "0002_idem_unique.sql", "0003_submission_scope.sql",
		"0004_supplier_norm.sql", "0005_attachment.sql", "0006_ledger_multi.sql", "0007_approval_core.sql",
		"0008_flow_task_release.sql", "0009_flow_task_order.sql", "0010_instance_ext_json.sql",
		"0011_flow_op_log_round.sql", "0013_callback_repair.sql"} {
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
	// ★ 期望值＝仓库内迁移文件数（新增迁移时同步此处；
	//   0004 Q20 supplier_norm、0005 附件元数据、0006 台账映射一对多 B47、0007 审批核心转向③、
	//   0008 t_flow_task 顺序会签 release_state/weight、0009 task_order 次序契约、
	//   0010 t_instance.ext_json 非规范字段、
	//   0011 t_flow_op_log.round ＋ 回调幂等键入 round、
	//   0013 回调链路修复（t_flow_op_log.message_id ＋ t_approval_def.feishu_code，docs/16 §5）、
	//   0014 t_notify_log.message_id（卡片刷新链路；写入者＝NotifySender.Send，R26 同批）。
	const wantMigrations = 13
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
