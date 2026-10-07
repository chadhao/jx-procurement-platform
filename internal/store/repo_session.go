package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SessionRow 会话行（t_session，N-077：会话落库 —— 原内存 map 重启即清空）。
type SessionRow struct {
	ID        string
	OpenID    string
	IssuedAt  time.Time
	ExpiresAt time.Time
	UpdatedAt time.Time
}

// InsertSession 写入新会话（Establish 路径；id 主键，重复 id 属碰撞即报错）。
func (d *DB) InsertSession(ctx context.Context, r SessionRow) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_session (id, open_id, issued_at, expires_at, updated_at)
VALUES (?,?,?,?,?)`,
		r.ID, r.OpenID, fmtTime(r.IssuedAt), fmtTime(r.ExpiresAt), fmtTime(r.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: 写入会话失败: %w", err)
	}
	return nil
}

// GetSession 按 id 读会话；不存在 ⇒ ErrNotFound（deny by default：查无即失效）。
func (d *DB) GetSession(ctx context.Context, id string) (SessionRow, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, open_id, issued_at, expires_at, updated_at FROM t_session WHERE id = ?`, id)
	var (
		r   SessionRow
		iss string
		exp string
		upd string
	)
	if err := row.Scan(&r.ID, &r.OpenID, &iss, &exp, &upd); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionRow{}, ErrNotFound
		}
		return SessionRow{}, fmt.Errorf("store: 读取会话失败: %w", err)
	}
	r.IssuedAt = parseTime(iss)
	r.ExpiresAt = parseTime(exp)
	r.UpdatedAt = parseTime(upd)
	return r, nil
}

// TouchSession 滑动续期（N-077 判据②）：刷新 expires_at 与 updated_at。
func (d *DB) TouchSession(ctx context.Context, id string, expiresAt, updatedAt time.Time) error {
	_, err := d.ExecContext(ctx, `
UPDATE t_session SET expires_at = ?, updated_at = ? WHERE id = ?`,
		fmtTime(expiresAt), fmtTime(updatedAt), id)
	if err != nil {
		return fmt.Errorf("store: 续期会话失败: %w", err)
	}
	return nil
}

// DeleteSession 销毁会话（Destroy 路径；落库后**重启仍生效**，N-077 判据⑤）。
func (d *DB) DeleteSession(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM t_session WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 删除会话失败: %w", err)
	}
	return nil
}

// PurgeExpiredSessions 顺带清理已过期行（Establish 写路径调用；表极小、幂等）。
func (d *DB) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM t_session WHERE expires_at < ?`, fmtTime(now))
	if err != nil {
		return 0, fmt.Errorf("store: 清理过期会话失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
