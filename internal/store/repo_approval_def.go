package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ---------- 三方审批定义注册表（t_approval_def，04a §1.1 / §3） ----------

// UpsertApprovalDef 幂等写入三方审批定义（approval_code 唯一）。
//
// ★ 幂等语义（04a §12.1「更新不产生重复定义」）：以 approval_code 为冲突键 → **重复注册即更新**；
// 另有 doc_type 唯一索引兜底，防止「同一单据类型被注册成两个定义」这一静默缺陷。
func (d *DB) UpsertApprovalDef(ctx context.Context, def *ApprovalDef) error {
	return upsertApprovalDef(ctx, d, def)
}

// UpsertApprovalDefTx 在事务内幂等写入三方审批定义。
func (d *DB) UpsertApprovalDefTx(ctx context.Context, tx *sql.Tx, def *ApprovalDef) error {
	return upsertApprovalDef(ctx, tx, def)
}

func upsertApprovalDef(ctx context.Context, q execer, def *ApprovalDef) error {
	if def == nil || def.ApprovalCode == "" || def.DocType == "" {
		return fmt.Errorf("store: 写入审批定义失败: approval_code/doc_type 不能为空")
	}
	version := def.DefVersion
	if version <= 0 {
		version = 1
	}
	_, err := q.ExecContext(ctx, `
INSERT INTO t_approval_def
  (approval_code, doc_type, name, group_name, visible_scope_json, create_link_pc, create_link_mobile,
   callback_url, callback_token, callback_key, form_summary_json, feishu_code, def_version, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(approval_code) DO UPDATE SET
  doc_type           = excluded.doc_type,
  name               = excluded.name,
  group_name         = COALESCE(NULLIF(excluded.group_name,''), t_approval_def.group_name),
  visible_scope_json = COALESCE(NULLIF(excluded.visible_scope_json,''), t_approval_def.visible_scope_json),
  create_link_pc     = COALESCE(NULLIF(excluded.create_link_pc,''), t_approval_def.create_link_pc),
  create_link_mobile = COALESCE(NULLIF(excluded.create_link_mobile,''), t_approval_def.create_link_mobile),
  callback_url       = COALESCE(NULLIF(excluded.callback_url,''), t_approval_def.callback_url),
  callback_token     = COALESCE(NULLIF(excluded.callback_token,''), t_approval_def.callback_token),
  callback_key       = COALESCE(NULLIF(excluded.callback_key,''), t_approval_def.callback_key),
  form_summary_json  = COALESCE(NULLIF(excluded.form_summary_json,''), t_approval_def.form_summary_json),
  feishu_code        = COALESCE(NULLIF(excluded.feishu_code,''), t_approval_def.feishu_code),
  def_version        = excluded.def_version,
  updated_at         = excluded.updated_at`,
		def.ApprovalCode, def.DocType, def.Name, nullStr(def.GroupName), nullStr(def.VisibleScopeJSON),
		nullStr(def.CreateLinkPC), nullStr(def.CreateLinkMobile), nullStr(def.CallbackURL),
		nullStr(def.CallbackToken), nullStr(def.CallbackKey), nullStr(def.FormSummaryJSON),
		nullStr(def.FeishuCode), version, fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 写入审批定义 %s 失败: %w", def.ApprovalCode, err)
	}
	return nil
}

// GetApprovalDef 按 approval_code 读取定义；不存在返回 ErrNotFound。
func (d *DB) GetApprovalDef(ctx context.Context, approvalCode string) (*ApprovalDef, error) {
	row := d.QueryRowContext(ctx, approvalDefSelectSQL+` WHERE approval_code = ?`, approvalCode)
	return scanApprovalDef(row)
}

// GetApprovalDefByDocType 按单据类型读取定义；不存在返回 ErrNotFound。
func (d *DB) GetApprovalDefByDocType(ctx context.Context, docType string) (*ApprovalDef, error) {
	row := d.QueryRowContext(ctx, approvalDefSelectSQL+` WHERE doc_type = ?`, docType)
	return scanApprovalDef(row)
}

// ListApprovalDefs 列出全部定义（按 doc_type 升序）。
func (d *DB) ListApprovalDefs(ctx context.Context) ([]ApprovalDef, error) {
	rows, err := d.QueryContext(ctx, approvalDefSelectSQL+` ORDER BY doc_type`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ApprovalDef
	for rows.Next() {
		def, err := scanApprovalDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *def)
	}
	return out, rows.Err()
}

// CountApprovalDefs 返回定义条数（供「11 类定义齐备」自检，04a §10 S7）。
func (d *DB) CountApprovalDefs(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_approval_def`).Scan(&n)
	return n, err
}

const approvalDefSelectSQL = `
SELECT approval_code, doc_type, name, COALESCE(group_name,''), COALESCE(visible_scope_json,''),
       COALESCE(create_link_pc,''), COALESCE(create_link_mobile,''), COALESCE(callback_url,''),
       COALESCE(callback_token,''), COALESCE(callback_key,''), COALESCE(form_summary_json,''),
       COALESCE(feishu_code,''), COALESCE(def_version,1), updated_at
FROM t_approval_def`

func scanApprovalDef(s interface {
	Scan(dest ...any) error
}) (*ApprovalDef, error) {
	var (
		def     ApprovalDef
		updated string
	)
	if err := s.Scan(&def.ApprovalCode, &def.DocType, &def.Name, &def.GroupName, &def.VisibleScopeJSON,
		&def.CreateLinkPC, &def.CreateLinkMobile, &def.CallbackURL, &def.CallbackToken, &def.CallbackKey,
		&def.FormSummaryJSON, &def.FeishuCode, &def.DefVersion, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	def.UpdatedAt = parseTime(updated)
	return &def, nil
}
