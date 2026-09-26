// Package storetest 提供测试用的临时 SQLite（已执行迁移）。
package storetest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// NewDB 创建临时数据库并执行迁移；测试结束自动清理。
func NewDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Count 返回某表满足条件的行数（供断言）。
func Count(t *testing.T, db *store.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("统计失败 %q: %v", query, err)
	}
	return n
}
