package store

// N-060 H1②：0020_ops_writable_refresh 语义 —— 只增不覆盖 ＋ 幂等（重放两遍）。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/migrations"
)

func TestMigration0020OnlyAddsAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mig0020.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("全量迁移失败: %v", err)
	}

	// ① 造「既有库 + 管理员改过」的行：旧种子名（提交日期/集团受理编号/付款完成日期）
	//    ＋ 一个管理员自定义键（绝不能被本迁移抹掉）。
	const oldWish = `["付款凭据号","提交日期","集团受理编号","付款完成日期","管理员自定义键"]`
	if _, err := db.ExecContext(ctx,
		`INSERT INTO t_permission_rule(resource, role, row_scope, writable_fields, updated_at)
		 VALUES ('ledger:*', '综合运营主管', 'ALL', ?, '2026-01-01')`, oldWish); err != nil {
		t.Fatalf("插入既有行失败: %v", err)
	}

	run0020 := func() {
		sqlBytes, err := migrations.FS.ReadFile("0020_ops_writable_refresh.sql")
		if err != nil {
			t.Fatalf("读取 0020 失败: %v", err)
		}
		if _, err := db.ExecContext(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("执行 0020 失败: %v", err)
		}
	}
	// ② 跑一遍：旧名换权威名 ＋ 四键并入 ＋ 管理员自定义键保留
	run0020()
	var wf string
	if err := db.QueryRowContext(ctx,
		`SELECT writable_fields FROM t_permission_rule WHERE resource='ledger:*' AND role='综合运营主管'`).
		Scan(&wf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"提交集团日期", "集团流程编号", "付款 / 报销完成日期",
		"移交凭证（签收）", "驳回原因与处置", "原件移交清单", "原件签收记录",
		"管理员自定义键", // ★ 只增不覆盖：管理员已改内容必须原样保留
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("迁移后缺 %q（只增不覆盖被破坏？）：%s", want, wf)
		}
	}
	for _, gone := range []string{`"提交日期"`, `"集团受理编号"`, `"付款完成日期"`} {
		if strings.Contains(wf, gone) {
			t.Errorf("旧名 %s 应已替换为权威名：%s", gone, wf)
		}
	}

	// ③ 再跑一遍（幂等）：内容不变（尤其 json_array_append 不得重复追加）
	before := wf
	run0020()
	var wf2 string
	if err := db.QueryRowContext(ctx,
		`SELECT writable_fields FROM t_permission_rule WHERE resource='ledger:*' AND role='综合运营主管'`).
		Scan(&wf2); err != nil {
		t.Fatal(err)
	}
	if wf2 != before {
		t.Errorf("重放 0020 后内容变化（幂等失败）：\n前=%s\n后=%s", before, wf2)
	}
	if n := strings.Count(wf2, "原件移交清单"); n != 1 {
		t.Errorf("原件移交清单 出现 %d 次，期望恰 1（append NOT LIKE 守卫失效？）", n)
	}
}
