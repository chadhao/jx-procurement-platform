package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---------- 台账·同步存档（只读，M4） ----------

// UpsertArchive 幂等写入台账存档行（(ledger_type, biz_no) 唯一，重放不产生重复）。
func (d *DB) UpsertArchive(ctx context.Context, q execer, a *LedgerArchive) error {
	return upsertArchive(ctx, q, a)
}

func upsertArchive(ctx context.Context, q execer, a *LedgerArchive) error {
	if q == nil {
		return ErrNotFound
	}
	_, err := q.ExecContext(ctx, `
INSERT INTO t_ledger_archive
  (ledger_type, biz_no, instance_code, source_doc_type, department, applicant_open_id, submitter_open_id,
   amount_cents, supplier, purpose_class_l1, purpose_class_l2, biz_date, ext_json, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(ledger_type, biz_no) DO UPDATE SET
  instance_code = COALESCE(NULLIF(excluded.instance_code,''), t_ledger_archive.instance_code),
  source_doc_type = COALESCE(NULLIF(excluded.source_doc_type,''), t_ledger_archive.source_doc_type),
  department = COALESCE(NULLIF(excluded.department,''), t_ledger_archive.department),
  applicant_open_id = COALESCE(NULLIF(excluded.applicant_open_id,''), t_ledger_archive.applicant_open_id),
  submitter_open_id = COALESCE(NULLIF(excluded.submitter_open_id,''), t_ledger_archive.submitter_open_id),
  amount_cents = COALESCE(excluded.amount_cents, t_ledger_archive.amount_cents),
  supplier = COALESCE(NULLIF(excluded.supplier,''), t_ledger_archive.supplier),
  purpose_class_l1 = COALESCE(NULLIF(excluded.purpose_class_l1,''), t_ledger_archive.purpose_class_l1),
  purpose_class_l2 = COALESCE(NULLIF(excluded.purpose_class_l2,''), t_ledger_archive.purpose_class_l2),
  biz_date = COALESCE(NULLIF(excluded.biz_date,''), t_ledger_archive.biz_date),
  ext_json = excluded.ext_json,
  updated_at = excluded.updated_at`,
		a.LedgerType, nullStr(a.BizNo), nullStr(a.InstanceCode), nullStr(a.SourceDocType),
		nullStr(a.Department), nullStr(a.ApplicantOpenID), nullStr(a.SubmitterOpenID), a.AmountCents,
		nullStr(a.Supplier), nullStr(a.PurposeClassL1), nullStr(a.PurposeClassL2), nullStr(a.BizDate),
		defaultStr(a.ExtJSON, "{}"), fmtTime(a.CreatedAt), fmtTime(a.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: 写入台账存档失败: %w", err)
	}
	return nil
}

// LedgerFilter 台账列表过滤条件（行级过滤由 RowSQL/RowArgs 注入）。
type LedgerFilter struct {
	LedgerType string
	BizNo      string
	Department string
	Supplier   string
	Status     string
	DateFrom   string
	DateTo     string
	RowSQL     string
	RowArgs    []any
	Limit      int
	Offset     int
}

const archiveSelectSQL = `
SELECT id, ledger_type, COALESCE(biz_no,''), COALESCE(instance_code,''), COALESCE(source_doc_type,''),
       COALESCE(department,''), COALESCE(applicant_open_id,''), COALESCE(submitter_open_id,''), amount_cents,
       COALESCE(supplier,''), COALESCE(purpose_class_l1,''), COALESCE(purpose_class_l2,''), COALESCE(biz_date,''),
       COALESCE(ext_json,'{}'), created_at, updated_at
FROM t_ledger_archive a`

// ListArchive 查询台账存档行；自动施加行级过滤（在 SQL 层，绝不在内存删，架构 §5.2）。
func (d *DB) ListArchive(ctx context.Context, f LedgerFilter) ([]LedgerArchive, int, error) {
	var (
		where []string
		args  []any
	)
	if f.LedgerType != "" {
		where = append(where, "ledger_type = ?")
		args = append(args, f.LedgerType)
	}
	if f.BizNo != "" {
		where = append(where, "biz_no = ?")
		args = append(args, f.BizNo)
	}
	if f.Department != "" {
		where = append(where, "department = ?")
		args = append(args, f.Department)
	}
	if f.Supplier != "" {
		where = append(where, "supplier = ?")
		args = append(args, f.Supplier)
	}
	if f.DateFrom != "" {
		where = append(where, "biz_date >= ?")
		args = append(args, f.DateFrom)
	}
	if f.DateTo != "" {
		where = append(where, "biz_date <= ?")
		args = append(args, f.DateTo)
	}
	if strings.TrimSpace(f.RowSQL) != "" {
		where = append(where, f.RowSQL)
		args = append(args, f.RowArgs...)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_ledger_archive a`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, archiveSelectSQL+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []LedgerArchive
	for rows.Next() {
		a, err := scanArchive(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *a)
	}
	return out, total, rows.Err()
}

// GetArchiveByID 按主键读取台账存档行。
func (d *DB) GetArchiveByID(ctx context.Context, id int64) (*LedgerArchive, error) {
	row := d.QueryRowContext(ctx, archiveSelectSQL+` WHERE id = ?`, id)
	return scanArchive(row)
}

func scanArchive(s interface {
	Scan(dest ...any) error
}) (*LedgerArchive, error) {
	var (
		a       LedgerArchive
		amount  sql.NullInt64
		created string
		updated string
	)
	if err := s.Scan(&a.ID, &a.LedgerType, &a.BizNo, &a.InstanceCode, &a.SourceDocType, &a.Department,
		&a.ApplicantOpenID, &a.SubmitterOpenID, &amount, &a.Supplier, &a.PurposeClassL1, &a.PurposeClassL2,
		&a.BizDate, &a.ExtJSON, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if amount.Valid {
		v := amount.Int64
		a.AmountCents = &v
	}
	a.CreatedAt = parseTime(created)
	a.UpdatedAt = parseTime(updated)
	return &a, nil
}

// ---------- 台账·运营表（可写，M4） ----------

// UpsertOpsByID 以业务单号 UPSERT 运营字段（天然幂等，接口约定 §8）。
func (d *DB) UpsertOps(ctx context.Context, o *LedgerOps) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_ledger_ops (ledger_type, biz_no, ops_json, updated_by, updated_at)
VALUES (?,?,?,?,?)
ON CONFLICT(ledger_type, biz_no) DO UPDATE SET
  ops_json = excluded.ops_json, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		o.LedgerType, o.BizNo, defaultStr(o.OpsJSON, "{}"), nullStr(o.UpdatedBy), fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 写入运营表失败: %w", err)
	}
	return nil
}

// GetOps 读取某业务单号的运营字段；不存在返回 ErrNotFound。
func (d *DB) GetOps(ctx context.Context, ledgerType, bizNo string) (*LedgerOps, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, ledger_type, biz_no, COALESCE(ops_json,'{}'), COALESCE(updated_by,''), updated_at
FROM t_ledger_ops WHERE ledger_type = ? AND biz_no = ?`, ledgerType, bizNo)
	var (
		o       LedgerOps
		updated string
	)
	if err := row.Scan(&o.ID, &o.LedgerType, &o.BizNo, &o.OpsJSON, &o.UpdatedBy, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	o.UpdatedAt = parseTime(updated)
	return &o, nil
}

// SumArchiveAmountBySupplierMonth 统计同供应商在指定月份的累计金额（分）。
// 用于「同供应商当月累计」防拆分公式列（FR-M4-04/05，含跨部门、跨品类）。
//
// ★ 月份参数格式为 **`YYYY-MM`**（如 `2026-09`）。`biz_date` 全系统统一为 `YYYY-MM-DD`
// （B32），故用 `LIKE 'YYYY-MM%'` 精确落到该月——**不要**再传 `YYMM` 之类的旧格式。
func (d *DB) SumArchiveAmountBySupplierMonth(ctx context.Context, ledgerType, supplier, month string) (int64, error) {
	month = strings.TrimSpace(month)
	if month == "" {
		return 0, nil
	}
	var sum sql.NullInt64
	err := d.QueryRowContext(ctx, `
SELECT COALESCE(SUM(amount_cents),0) FROM t_ledger_archive
WHERE ledger_type = ? AND supplier = ? AND biz_date LIKE ?`,
		ledgerType, supplier, month+"%").Scan(&sum)
	if err != nil {
		return 0, err
	}
	return sum.Int64, nil
}
