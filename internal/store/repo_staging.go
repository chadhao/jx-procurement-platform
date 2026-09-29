package store

// 附件暂存（M6 / 决策 D4）：上传占位 → 提交事务内绑定迁入 t_attachment。
// ★ 行级权限与 t_attachment 的约束决定了「先暂存、后绑定」的两段式（见 0016 头注）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AttachmentStaging 暂存行。
type AttachmentStaging struct {
	FileID      string
	OwnerOpenID string
	FileName    string
	SizeBytes   int64
	StorageKey  string
	StorageKind string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	BoundBizNo  string
}

// ErrStagingNotBindable 暂存件不可绑定（不存在 / 非本人 / 已绑定 / 已过期）。
var ErrStagingNotBindable = errors.New("store: 暂存附件不可绑定")

// PutStaging 登记暂存行（同 file_id 幂等覆盖 —— file_id 为随机主键，正常不撞）。
func (d *DB) PutStaging(ctx context.Context, row *AttachmentStaging) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_attachment_staging
  (file_id, owner_open_id, file_name, size_bytes, storage_key, storage_kind, created_at, expires_at, bound_biz_no)
VALUES (?,?,?,?,?,?,?,?,NULL)
ON CONFLICT(file_id) DO UPDATE SET
  file_name = excluded.file_name,
  size_bytes = excluded.size_bytes,
  storage_key = excluded.storage_key,
  storage_kind = excluded.storage_kind,
  expires_at = excluded.expires_at`,
		row.FileID, row.OwnerOpenID, row.FileName, row.SizeBytes,
		row.StorageKey, row.StorageKind, fmtTime(row.CreatedAt), fmtTime(row.ExpiresAt))
	if err != nil {
		return fmt.Errorf("store: 写入附件暂存失败: %w", err)
	}
	return nil
}

// GetStaging 读取暂存行（owner-only 读取用）。
func (d *DB) GetStaging(ctx context.Context, fileID string) (*AttachmentStaging, error) {
	row := &AttachmentStaging{}
	var bound sql.NullString
	var created, expires string
	err := d.QueryRowContext(ctx, `
SELECT file_id, owner_open_id, file_name, size_bytes, storage_key, storage_kind,
       created_at, expires_at, bound_biz_no
FROM t_attachment_staging WHERE file_id = ?`, fileID).Scan(
		&row.FileID, &row.OwnerOpenID, &row.FileName, &row.SizeBytes,
		&row.StorageKey, &row.StorageKind, &created, &expires, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询附件暂存失败: %w", err)
	}
	row.BoundBizNo = bound.String
	return row, nil
}

// BindStagingTx 提交事务内把暂存件迁入 t_attachment 并回填 bound_biz_no。
// 返回迁入行数（0 ⇒ 调用方应整体失败：不可绑定的原因在 guard 条件里）。
func (d *DB) BindStagingTx(ctx context.Context, tx *sql.Tx, fileID, ownerOpenID, instanceCode, bizNo string) (int64, error) {
	now := fmtTime(time.Now())
	res, err := tx.ExecContext(ctx, `
INSERT INTO t_attachment
  (file_id, instance_code, biz_no, field_id, file_name, size_bytes,
   storage_key, storage_kind, fetched_at, created_at, updated_at)
SELECT s.file_id, ?, ?, NULL, s.file_name, s.size_bytes,
       s.storage_key, s.storage_kind, NULL, s.created_at, ?
FROM t_attachment_staging s
WHERE s.file_id = ? AND s.owner_open_id = ?
  AND s.bound_biz_no IS NULL AND s.expires_at > ?`,
		instanceCode, bizNo, now, fileID, ownerOpenID, now)
	if err != nil {
		return 0, fmt.Errorf("store: 绑定附件（迁入 t_attachment）失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取绑定行数失败: %w", err)
	}
	if n == 0 {
		return 0, fmt.Errorf("%w: %s（不存在/非本人/已绑定/已过期）", ErrStagingNotBindable, fileID)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE t_attachment_staging SET bound_biz_no = ?
WHERE file_id = ? AND owner_open_id = ? AND bound_biz_no IS NULL`, bizNo, fileID, ownerOpenID); err != nil {
		return 0, fmt.Errorf("store: 回填暂存绑定状态失败: %w", err)
	}
	return n, nil
}

// PurgeExpiredStaging 惰性清理已过期且未绑定的暂存行（上传时顺手调用，尽力而为）。
func (d *DB) PurgeExpiredStaging(ctx context.Context, now time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `
DELETE FROM t_attachment_staging
WHERE bound_biz_no IS NULL AND expires_at <= ?`, fmtTime(now))
	if err != nil {
		return 0, fmt.Errorf("store: 清理过期附件暂存失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取清理行数失败: %w", err)
	}
	return n, nil
}
