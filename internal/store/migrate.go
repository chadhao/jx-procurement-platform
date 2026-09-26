package store

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/chadhao/jx-procurement-platform/migrations"
)

// schemaMigrationsTable 简易版本表（不引第三方迁移库，见架构 §2）。
const schemaMigrationsTable = `
CREATE TABLE IF NOT EXISTS t_schema_migrations (
  version    TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL
);`

// Migrate 按文件名升序执行未应用的迁移脚本；每个脚本在独立事务内执行并登记版本。
func Migrate(ctx context.Context, db *DB) error {
	if _, err := db.ExecContext(ctx, schemaMigrationsTable); err != nil {
		return fmt.Errorf("store: 创建版本表失败: %w", err)
	}

	files, err := sqlFiles()
	if err != nil {
		return err
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	for _, name := range files {
		if applied[name] {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("store: 读取迁移 %s 失败: %w", name, err)
		}
		if err := applyOne(ctx, db, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// sqlFiles 列出 embed 内的 .sql 脚本并按名升序返回。
func sqlFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("store: 读取迁移目录失败: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, path.Base(e.Name()))
		}
	}
	sort.Strings(names)
	return names, nil
}

// appliedVersions 读取已应用版本集合。
func appliedVersions(ctx context.Context, db *DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM t_schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: 读取版本表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// applyOne 在一个事务内执行单个迁移脚本并登记版本。
func applyOne(ctx context.Context, db *DB, version, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 迁移 %s 开启事务失败: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: 迁移 %s 执行失败: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO t_schema_migrations(version, applied_at) VALUES(?, ?)`,
		version, fmtTime(timeNow().UTC())); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: 迁移 %s 登记版本失败: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 迁移 %s 提交失败: %w", version, err)
	}
	return nil
}
