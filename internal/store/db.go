// Package store 是 SQLite 存储层（Repository）。
// 采用纯 Go 驱动 modernc.org/sqlite（driver 名 "sqlite"）——本机无 gcc，严禁需要 cgo 的 mattn/go-sqlite3。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	// 纯 Go SQLite 驱动（driver 名 "sqlite"）。
	_ "modernc.org/sqlite"
)

// DB 包装 *sql.DB，暴露仓储方法。
type DB struct {
	*sql.DB
}

// Open 打开 SQLite（WAL + busy_timeout + foreign_keys=ON）并做一次连通性校验。
//
// 连接数固定为 1：SQLite 为单写者，串行化可彻底规避 "database is locked"，
// 也保证同一实例的作业处理与状态收敛按序执行（S0/S1 负载远未触及瓶颈）。
func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("store: 数据库路径为空")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: 创建数据目录失败: %w", err)
		}
	}
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: 打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: 连接数据库失败: %w", err)
	}
	return &DB{DB: db}, nil
}

// buildDSN 构造带 PRAGMA 的 DSN。modernc 驱动支持重复的 _pragma 参数。
func buildDSN(path string) string {
	pragmas := []string{
		"busy_timeout(5000)",
		"journal_mode(WAL)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
	}
	parts := make([]string, 0, len(pragmas))
	for _, p := range pragmas {
		parts = append(parts, "_pragma="+p)
	}
	return filepath.ToSlash(path) + "?" + strings.Join(parts, "&")
}

// WritableProbe 写探针：在回滚事务中向审计表写入再回滚，验证数据库可写（架构 §7.3 自检第 3 项）。
// 失败即视为「能收事件但写不进」的半可用态，启动方应拒绝启动（TC-28）。
func (d *DB) WritableProbe(ctx context.Context) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启写探针事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO t_audit_log(action, result, created_at) VALUES('__write_probe__', 'allow', ?)`,
		fmtTime(timeNow().UTC())); err != nil {
		return fmt.Errorf("store: 写探针失败（数据库不可写）: %w", err)
	}
	return nil
}
