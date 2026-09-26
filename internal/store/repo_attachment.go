package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---------- 附件元数据（B39，M0/FR-M0-09） ----------
//
// ★ 分工：**元数据入库时登记**（只 INSERT，零网络 IO，不占 3 秒事件窗口）；
// **文件本体按需拉取**（下载 / 生成凭证包时），拉到后写对象存储并回填 storage_key。
//
// ★ 行级权限**不在此表冗余身份列**：一律以 `instance_code` 回查 `t_instance` 的可见性，
// 避免"两处身份列不同步"。这是与 `t_submission` 相反的选择 —— 那里没有权威来源可回查，
// 这里**有**（t_instance 就是权威），所以不冗余。

// Attachment 附件元数据行。
type Attachment struct {
	ID           int64
	FileID       string
	InstanceCode string
	BizNo        string
	FieldID      string
	FileName     string
	SizeBytes    *int64
	StorageKey   string
	StorageKind  string
	FetchedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UpsertAttachment 幂等登记附件元数据（同 (instance_code, file_id) 覆盖更新非空字段）。
//
// ★ 关键：**不覆盖已拉取信息**（storage_key / fetched_at），否则每次事件重放都会把
// "已缓存"标记清掉，导致重复下载（浪费 API 配额，且 1000 次/分钟的限频很容易被撞）。
func (d *DB) UpsertAttachment(ctx context.Context, a *Attachment) error {
	return upsertAttachment(ctx, d, a)
}

// UpsertAttachmentTx 在事务内幂等登记附件元数据（入库路径使用）。
func (d *DB) UpsertAttachmentTx(ctx context.Context, tx *sql.Tx, a *Attachment) error {
	return upsertAttachment(ctx, tx, a)
}

func upsertAttachment(ctx context.Context, q execer, a *Attachment) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO t_attachment
  (file_id, instance_code, biz_no, field_id, file_name, size_bytes, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(instance_code, file_id) DO UPDATE SET
  biz_no     = COALESCE(NULLIF(excluded.biz_no,''),     t_attachment.biz_no),
  field_id   = COALESCE(NULLIF(excluded.field_id,''),   t_attachment.field_id),
  file_name  = COALESCE(NULLIF(excluded.file_name,''),  t_attachment.file_name),
  size_bytes = COALESCE(excluded.size_bytes,            t_attachment.size_bytes),
  updated_at = excluded.updated_at`,
		a.FileID, a.InstanceCode, nullStr(a.BizNo), nullStr(a.FieldID), nullStr(a.FileName),
		a.SizeBytes, fmtTime(timeNow().UTC()), fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 登记附件元数据失败: %w", err)
	}
	return nil
}

const attachmentSelectSQL = `
SELECT id, file_id, instance_code, COALESCE(biz_no,''), COALESCE(field_id,''), COALESCE(file_name,''),
       size_bytes, COALESCE(storage_key,''), COALESCE(storage_kind,''), COALESCE(fetched_at,''),
       created_at, updated_at
FROM t_attachment`

// GetAttachmentByFileID 按 file_id 读取附件元数据（同一 file_id 出现在多实例时取最早一行）。
func (d *DB) GetAttachmentByFileID(ctx context.Context, fileID string) (*Attachment, error) {
	row := d.QueryRowContext(ctx, attachmentSelectSQL+` WHERE file_id = ? ORDER BY id ASC LIMIT 1`, fileID)
	return scanAttachment(row)
}

// ListAttachmentsByInstance 列出某实例的附件元数据（按 id 升序，稳定）。
func (d *DB) ListAttachmentsByInstance(ctx context.Context, instanceCode string) ([]Attachment, error) {
	rows, err := d.QueryContext(ctx, attachmentSelectSQL+` WHERE instance_code = ? ORDER BY id ASC`, instanceCode)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// MarkAttachmentFetched 回填「已落主存」信息（下载成功后调用）。
func (d *DB) MarkAttachmentFetched(ctx context.Context, id int64, storageKey, storageKind string) error {
	now := fmtTime(timeNow().UTC())
	if _, err := d.ExecContext(ctx, `
UPDATE t_attachment SET storage_key = ?, storage_kind = ?, fetched_at = ?, updated_at = ?
WHERE id = ?`, strings.TrimSpace(storageKey), strings.TrimSpace(storageKind), now, now, id); err != nil {
		return fmt.Errorf("store: 回填附件存储信息失败: %w", err)
	}
	return nil
}

func scanAttachment(s interface {
	Scan(dest ...any) error
}) (*Attachment, error) {
	var (
		a       Attachment
		size    sql.NullInt64
		fetched string
		created string
		updated string
	)
	if err := s.Scan(&a.ID, &a.FileID, &a.InstanceCode, &a.BizNo, &a.FieldID, &a.FileName,
		&size, &a.StorageKey, &a.StorageKind, &fetched, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if size.Valid {
		v := size.Int64
		a.SizeBytes = &v
	}
	a.FetchedAt = parseTimePtr(fetched)
	a.CreatedAt = parseTime(created)
	a.UpdatedAt = parseTime(updated)
	return &a, nil
}
