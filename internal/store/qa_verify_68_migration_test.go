package store

// qa_verify_68_migration_test.go —— 独立验证者（qa-verify-68）验证测试**收编入库**版（迁移半）。
//
// 来源与依据：
//   - 首轮独立验证（qa-verify-68，worktree@e9f69fb）第 4 项：0011（回调幂等键入 round）
//     的**旧库升级路径** —— 模拟已上线库（0001–0010）→ 应用 0011。
//   - 定案 #77（改前红探针在 worktree 取证）不适用于本文件：升级路径属「能力守门」，
//     非「缺口复现翻转」；其探针形态（升级前 round 列不存在 → 升级后存在）已内嵌于用例①。
//
// 断言（与验证报告逐条对应）：
//   ① 存量 t_flow_op_log 行 round=0（DEFAULT 0 回填）；
//   ② ★ PRAGMA index_info(ux_flow_op_callback) 证明索引确含全部 4 列
//      (biz_no, task_id, op_type, round) —— 索引重建失败＝幂等键退化回 3 列＝#68 静默缺陷复发温床；
//   ③ 历史行**不挡**新一轮（round=1/2 可写入），且同轮幂等仍生效；
//   ④ 全量 Migrate 幂等（重复执行无错）。

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/chadhao/jx-procurement-platform/migrations"
)

// qav68ApplyThrough0010 建库并只应用 0001–0010（模拟已上线旧库）。
func qav68ApplyThrough0010(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, schemaMigrationsTable); err != nil {
		t.Fatalf("创建版本表失败: %v", err)
	}
	files, err := sqlFiles()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, name := range files {
		if name >= "0011" {
			break
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyOne(ctx, db, name, string(body)); err != nil {
			t.Fatalf("应用 %s 失败: %v", name, err)
		}
	}
}

func TestQAV68Upgrade0011_OldRowsRound0AndIndexRebuilt(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	qav68ApplyThrough0010(t, db)

	// 升级前：round 列不存在；写入一条「历史」APPROVE 留痕（0001–0010 语义下无 round）。
	if cols := qaColumns(t, db, "t_flow_op_log"); cols["round"] {
		t.Fatalf("升级前不应存在 round 列: %v", cols)
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO t_flow_op_log (biz_no, task_id, op_type, actor_open_id, from_status, to_status, created_at)
VALUES ('BIZ-OLD', 'T-OLD', 'APPROVE', 'ou_old', 'PENDING', 'APPROVE', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("写入历史留痕失败: %v", err)
	}

	// ── 应用 0011 ──
	body, err := migrations.FS.ReadFile("0011_flow_op_log_round.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyOne(ctx, db, "0011_flow_op_log_round.sql", string(body)); err != nil {
		t.Fatalf("★ 升级 0011 失败: %v", err)
	}

	// ① 存量行 round = 0。
	var round int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(round,-1) FROM t_flow_op_log WHERE biz_no='BIZ-OLD'`).Scan(&round); err != nil {
		t.Fatal(err)
	}
	if round != 0 {
		t.Fatalf("存量行 round = %d, 期望 0（DEFAULT 0 回填缺失）", round)
	}

	// ② 索引重建：ux_flow_op_callback 必须恰含 4 列 biz_no/task_id/op_type/round。
	idxCols := map[string]bool{}
	rows, err := db.QueryContext(ctx, `PRAGMA index_info(ux_flow_op_callback)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int
		var name, col string
		if err := rows.Scan(&seq, &name, &col); err != nil {
			t.Fatal(err)
		}
		idxCols[col] = true
	}
	rows.Close()
	for _, want := range []string{"biz_no", "task_id", "op_type", "round"} {
		if !idxCols[want] {
			t.Fatalf("★ 索引 ux_flow_op_callback 缺列 %s（重建失败＝幂等键仍为 3 列）: %v", want, idxCols)
		}
	}

	// ③ 历史行不挡新一轮：round=1 可写入、同轮重复被吸收、round=2 可写入。
	insWithRound := func(r int) bool {
		res, err := db.ExecContext(ctx, `
INSERT OR IGNORE INTO t_flow_op_log (biz_no, task_id, op_type, round, actor_open_id, created_at)
VALUES ('BIZ-OLD', 'T-OLD', 'APPROVE', ?, 'ou_old', '2026-01-02T00:00:00Z')`, r)
		if err != nil {
			t.Fatalf("写入 round=%d 失败: %v", r, err)
		}
		n, _ := res.RowsAffected()
		return n > 0
	}
	if !insWithRound(1) {
		t.Fatalf("★ 历史行挡住了新一轮 round=1 写入（升级后缺陷未消除）")
	}
	if insWithRound(1) {
		t.Fatalf("同轮 round=1 重复写入未被幂等吸收（唯一索引未生效）")
	}
	if !insWithRound(2) {
		t.Fatalf("round=2 写入失败（新一轮被挡）")
	}
}

func TestQAV68MigrateIdempotentTwice(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "full.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, db); err != nil {
			t.Fatalf("第 %d 次 Migrate 失败: %v", i+1, err)
		}
	}
	if cols := qaColumns(t, db, "t_flow_op_log"); !cols["round"] {
		t.Fatalf("全量迁移后 t_flow_op_log 应含 round 列: %v", cols)
	}
}
