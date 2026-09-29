package store

// 批 0 · A 批验收：A1 审计默认排除幂等簿记 · A2 启动回收卡死 RUNNING 作业。

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListAuditExcludesIdemBookkeeping(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "audit-a1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	// 业务操作 + 两条幂等簿记
	for _, a := range []AuditLogRow{
		{ActorOpenID: "ou_x", Action: "submit", Resource: "approval", TargetID: "BA-2609-0001", Result: "allow", CreatedAt: time.Now()},
		{ActorOpenID: "ou_x", Action: "submission_idem", Resource: "submission", TargetID: "k1", Result: "allow", CreatedAt: time.Now()},
		{ActorOpenID: "ou_x", Action: "approval_submit_idem", Resource: "approval", TargetID: "k2", Result: "allow", CreatedAt: time.Now()},
	} {
		row := a
		if err := db.InsertAudit(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}

	// ① 默认查询：幂等簿记不出现（审计页只看业务操作）
	items, total, err := db.ListAudit(ctx, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Action != "submit" {
		t.Errorf("默认审计查询应只有 submit，实为 total=%d len=%d（%v）", total, len(items), items)
	}

	// ② 显式按 action 过滤：排障口放行
	items, _, err = db.ListAudit(ctx, AuditFilter{Action: "approval_submit_idem"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("显式查询 approval_submit_idem 应命中 1 条，实为 %d", len(items))
	}
}

func TestReclaimStaleRunningOnStartup(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "reclaim-a2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	// 造一条 RUNNING（模拟上一进程崩溃残留）
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_worker_job (inbox_id, job_type, state, attempts, created_at, updated_at)
VALUES (1, 'org_sync', 'RUNNING', 1, ?, ?)`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	// 造一条 QUEUED（不受影响）
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_worker_job (inbox_id, job_type, state, attempts, created_at, updated_at)
VALUES (2, 'org_sync', 'QUEUED', 0, ?, ?)`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	n, err := db.ReclaimStaleRunning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("回收行数 = %d，应为 1（只有 RUNNING）", n)
	}
	rows, err := db.QueryContext(ctx, `SELECT state FROM t_worker_job ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var states []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		states = append(states, s)
	}
	if len(states) != 2 || states[0] != "QUEUED" || states[1] != "QUEUED" {
		t.Errorf("回收后状态 = %v，应为 [QUEUED QUEUED]", states)
	}
	// 幂等：再跑一次无行可回收
	n2, err := db.ReclaimStaleRunning(ctx)
	if err != nil || n2 != 0 {
		t.Errorf("重复回收应 0 行（n=%d err=%v）", n2, err)
	}
}
