package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---------- 审计日志（M7） ----------

// InsertAudit 写入一条审计日志（deny / export / replay / login 等均留痕）。
func (d *DB) InsertAudit(ctx context.Context, a *AuditLogRow) error {
	return insertAudit(ctx, d, a)
}

// InsertAuditTx 在事务内写入一条审计日志。
//
// ★ 为什么需要 tx 版：单实例 + `SetMaxOpenConns(1)` → 事务持有唯一连接期间，
// 若用 `InsertAudit`（走 *sql.DB）会**等待连接 → 自锁**。事务内审计必须走本方法。
func (d *DB) InsertAuditTx(ctx context.Context, tx *sql.Tx, a *AuditLogRow) error {
	return insertAudit(ctx, tx, a)
}

func insertAudit(ctx context.Context, q execer, a *AuditLogRow) error {
	created := a.CreatedAt
	if created.IsZero() {
		created = timeNow().UTC()
	}
	_, err := q.ExecContext(ctx, `
INSERT INTO t_audit_log
  (actor_open_id, actor_role, action, resource, target_id, result, detail_json, feishu_log_id, ip, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?)`,
		nullStr(a.ActorOpenID), nullStr(a.ActorRole), a.Action, nullStr(a.Resource), nullStr(a.TargetID),
		nullStr(a.Result), nullStr(a.DetailJSON), nullStr(a.FeishuLogID), nullStr(a.IP), fmtTime(created))
	if err != nil {
		return fmt.Errorf("store: 写入审计日志失败: %w", err)
	}
	return nil
}

// AuditFilter 审计日志查询条件。
type AuditFilter struct {
	ActorOpenID string
	Role        string
	Action      string
	Resource    string
	Result      string
	Limit       int
	Offset      int
}

// ListAudit 查询审计日志。
func (d *DB) ListAudit(ctx context.Context, f AuditFilter) ([]AuditLogRow, int, error) {
	var (
		where []string
		args  []any
	)
	if f.ActorOpenID != "" {
		where = append(where, "actor_open_id = ?")
		args = append(args, f.ActorOpenID)
	}
	if f.Role != "" {
		where = append(where, "actor_role = ?")
		args = append(args, f.Role)
	}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	} else {
		// A1：幂等簿记（submission_idem / approval_submit_idem）是**技术占位**而非业务操作，
		// 默认不进审计查询（否则运维在审计页看到一堆"假操作"）；幂等判重走各自 repo 的
		// 独立 SELECT、不经本方法。★ 显式按 action 过滤时放行（运维排障口）。
		where = append(where, "action NOT IN ('submission_idem', 'approval_submit_idem')")
	}
	if f.Resource != "" {
		where = append(where, "resource = ?")
		args = append(args, f.Resource)
	}
	if f.Result != "" {
		where = append(where, "result = ?")
		args = append(args, f.Result)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_audit_log`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
SELECT id, COALESCE(actor_open_id,''), COALESCE(actor_role,''), action, COALESCE(resource,''),
       COALESCE(target_id,''), COALESCE(result,''), COALESCE(detail_json,''), COALESCE(feishu_log_id,''),
       COALESCE(ip,''), created_at
FROM t_audit_log`+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []AuditLogRow
	for rows.Next() {
		var (
			a       AuditLogRow
			created string
		)
		if err := rows.Scan(&a.ID, &a.ActorOpenID, &a.ActorRole, &a.Action, &a.Resource, &a.TargetID,
			&a.Result, &a.DetailJSON, &a.FeishuLogID, &a.IP, &created); err != nil {
			return nil, 0, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// ---------- 报送登记（M6） ----------

// CreateSubmission 新建报送登记，返回自增 id。
func (d *DB) CreateSubmission(ctx context.Context, s *Submission) (int64, error) {
	now := timeNow().UTC()
	_, err := d.ExecContext(ctx, `
INSERT INTO t_submission
  (biz_no, subject_type, amount_cents, pay_method, hn_finish_date, submit_date, receipt_ref, submit_state,
   grp_accept_no, grp_state, paid_date, reject_reason,
   department, applicant_open_id, assigned_open_id, acceptors,
   created_by, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nullStr(s.BizNo), nullStr(s.SubjectType), s.AmountCents, nullStr(s.PayMethod),
		nullStr(s.HNFinishDate), nullStr(s.SubmitDate), nullStr(s.ReceiptRef), s.SubmitState,
		nullStr(s.GrpAcceptNo), nullStr(s.GrpState), nullStr(s.PaidDate), nullStr(s.RejectReason),
		nullStr(s.Department), nullStr(s.ApplicantOpenID), nullStr(s.AssignedOpenID), nullStr(s.Acceptors),
		nullStr(s.CreatedBy), fmtTime(now), fmtTime(now))
	if err != nil {
		return 0, fmt.Errorf("store: 新建报送登记失败: %w", err)
	}
	var id int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM t_submission WHERE rowid = last_insert_rowid()`).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// GetSubmission 按 id 读取报送登记。
func (d *DB) GetSubmission(ctx context.Context, id int64) (*Submission, error) {
	row := d.QueryRowContext(ctx, submissionSelectSQL+` WHERE id = ?`, id)
	return scanSubmission(row)
}

// SubmissionFilter 报送列表过滤条件。
type SubmissionFilter struct {
	State  string
	Period string
	Limit  int
	Offset int
}

// ListSubmissions 查询报送登记列表。
func (d *DB) ListSubmissions(ctx context.Context, f SubmissionFilter) ([]Submission, int, error) {
	var (
		where []string
		args  []any
	)
	if f.State != "" {
		where = append(where, "submit_state = ?")
		args = append(args, f.State)
	}
	if f.Period != "" {
		where = append(where, "submit_date LIKE ?")
		args = append(args, f.Period+"%")
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_submission`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, submissionSelectSQL+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []Submission
	for rows.Next() {
		s, err := scanSubmission(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *s)
	}
	return out, total, rows.Err()
}

const submissionSelectSQL = `
SELECT id, COALESCE(biz_no,''), COALESCE(subject_type,''), amount_cents, COALESCE(pay_method,''),
       COALESCE(hn_finish_date,''), COALESCE(submit_date,''), COALESCE(receipt_ref,''), submit_state,
       COALESCE(grp_accept_no,''), COALESCE(grp_state,''), COALESCE(paid_date,''), COALESCE(reject_reason,''),
       COALESCE(department,''), COALESCE(applicant_open_id,''), COALESCE(assigned_open_id,''), COALESCE(acceptors,''),
       COALESCE(created_by,''), created_at, updated_at
FROM t_submission`

func scanSubmission(s interface {
	Scan(dest ...any) error
}) (*Submission, error) {
	var (
		sub     Submission
		amount  sql.NullInt64
		created string
		updated string
	)
	if err := s.Scan(&sub.ID, &sub.BizNo, &sub.SubjectType, &amount, &sub.PayMethod, &sub.HNFinishDate,
		&sub.SubmitDate, &sub.ReceiptRef, &sub.SubmitState, &sub.GrpAcceptNo, &sub.GrpState, &sub.PaidDate,
		&sub.RejectReason, &sub.Department, &sub.ApplicantOpenID, &sub.AssignedOpenID, &sub.Acceptors,
		&sub.CreatedBy, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if amount.Valid {
		v := amount.Int64
		sub.AmountCents = &v
	}
	sub.CreatedAt = parseTime(created)
	sub.UpdatedAt = parseTime(updated)
	return &sub, nil
}

// InsertSubmissionItem 写入报送关联单据。
func (d *DB) InsertSubmissionItem(ctx context.Context, it SubmissionItem) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO t_submission_item (submission_id, item_biz_no, item_type) VALUES (?,?,?)`,
		it.SubmissionID, it.ItemBizNo, nullStr(it.ItemType))
	return err
}

// ListSubmissionItems 读取报送关联单据。
func (d *DB) ListSubmissionItems(ctx context.Context, submissionID int64) ([]SubmissionItem, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, submission_id, item_biz_no, COALESCE(item_type,'') FROM t_submission_item WHERE submission_id = ? ORDER BY id`,
		submissionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SubmissionItem
	for rows.Next() {
		var it SubmissionItem
		if err := rows.Scan(&it.ID, &it.SubmissionID, &it.ItemBizNo, &it.ItemType); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ---------- 备付金（M1） ----------

// InsertPettyCashReceipt 登记备付金签领。
func (d *DB) InsertPettyCashReceipt(ctx context.Context, bizNo, instanceCode, receiverOpenID, receiverName string, amountCents int64, receivedDate, createdBy string) (int64, error) {
	res, err := d.ExecContext(ctx, `
INSERT INTO t_petty_cash_receipt
  (biz_no, instance_code, receiver_open_id, receiver_name, amount_cents, received_date, created_by, created_at)
VALUES (?,?,?,?,?,?,?,?)`,
		nullStr(bizNo), nullStr(instanceCode), nullStr(receiverOpenID), nullStr(receiverName),
		amountCents, receivedDate, nullStr(createdBy), fmtTime(timeNow().UTC()))
	if err != nil {
		return 0, fmt.Errorf("store: 登记备付金签领失败: %w", err)
	}
	return res.LastInsertId()
}

// UpsertPettyCashClose 按月核销备付金（唯一账期一条）。
func (d *DB) UpsertPettyCashClose(ctx context.Context, period string, issued, spent, balance int64, remark, createdBy string) error {
	_, err := d.ExecContext(ctx, `
INSERT INTO t_petty_cash_close (period, issued_cents, spent_cents, balance_cents, remark, created_by, created_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(period) DO UPDATE SET
  issued_cents = excluded.issued_cents, spent_cents = excluded.spent_cents,
  balance_cents = excluded.balance_cents, remark = excluded.remark`,
		period, issued, spent, balance, nullStr(remark), nullStr(createdBy), fmtTime(timeNow().UTC()))
	if err != nil {
		return fmt.Errorf("store: 备付金月核销失败: %w", err)
	}
	return nil
}

// GetPettyCashBalance 读取某账期核销后余额；不存在返回 ErrNotFound。
func (d *DB) GetPettyCashBalance(ctx context.Context, period string) (int64, error) {
	var bal sql.NullInt64
	if err := d.QueryRowContext(ctx,
		`SELECT balance_cents FROM t_petty_cash_close WHERE period = ?`, period).Scan(&bal); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return bal.Int64, nil
}

// InsertExpenseTrack 集团报销跟踪表人工登记（FR-M1-02）。
func (d *DB) InsertExpenseTrack(ctx context.Context, srcBizNo, applicantOpenID, department string, actualCents int64, invoiceCount int, reviewState, handoverDate string) (int64, error) {
	now := fmtTime(timeNow().UTC())
	res, err := d.ExecContext(ctx, `
INSERT INTO t_expense_track
  (src_biz_no, applicant_open_id, department, actual_cents, invoice_count, review_state, handover_date, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
		nullStr(srcBizNo), nullStr(applicantOpenID), nullStr(department), actualCents, invoiceCount,
		nullStr(reviewState), nullStr(handoverDate), now, now)
	if err != nil {
		return 0, fmt.Errorf("store: 登记集团报销跟踪失败: %w", err)
	}
	return res.LastInsertId()
}

// ExpenseTrackFilter 集团报销跟踪表查询条件。
type ExpenseTrackFilter struct {
	Department  string
	ReviewState string
	SrcBizNo    string
	Limit       int
	Offset      int
}

// ListExpenseTracks 查询集团报销跟踪表（人工登记的审批外数据，FR-M1-02）。
func (d *DB) ListExpenseTracks(ctx context.Context, f ExpenseTrackFilter) ([]ExpenseTrack, int64, error) {
	where := []string{"1=1"}
	args := []any{}
	if s := strings.TrimSpace(f.Department); s != "" {
		where = append(where, "department = ?")
		args = append(args, s)
	}
	if s := strings.TrimSpace(f.ReviewState); s != "" {
		where = append(where, "review_state = ?")
		args = append(args, s)
	}
	if s := strings.TrimSpace(f.SrcBizNo); s != "" {
		where = append(where, "src_biz_no = ?")
		args = append(args, s)
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	var total int64
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM t_expense_track"+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计集团报销跟踪失败: %w", err)
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	pageArgs := append(append([]any{}, args...), limit, f.Offset)
	rows, err := d.QueryContext(ctx, `
SELECT id, COALESCE(src_biz_no,''), COALESCE(applicant_open_id,''), COALESCE(department,''),
       actual_cents, invoice_count, COALESCE(review_state,''), COALESCE(handover_date,''),
       COALESCE(paid_date,''), paid_cents, COALESCE(overrun_note,''), COALESCE(created_by,''),
       created_at, updated_at
FROM t_expense_track`+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询集团报销跟踪失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ExpenseTrack
	for rows.Next() {
		var (
			e       ExpenseTrack
			actual  sql.NullInt64
			invoice sql.NullInt64
			paid    sql.NullInt64
			created string
			updated string
		)
		if err := rows.Scan(&e.ID, &e.SrcBizNo, &e.ApplicantOpenID, &e.Department, &actual, &invoice,
			&e.ReviewState, &e.HandoverDate, &e.PaidDate, &paid, &e.OverrunNote, &e.CreatedBy,
			&created, &updated); err != nil {
			return nil, 0, err
		}
		if actual.Valid {
			v := actual.Int64
			e.ActualCents = &v
		}
		if invoice.Valid {
			v := int(invoice.Int64)
			e.InvoiceCount = &v
		}
		if paid.Valid {
			v := paid.Int64
			e.PaidCents = &v
		}
		e.CreatedAt = parseTime(created)
		e.UpdatedAt = parseTime(updated)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// ExpenseTrackUpdate 集团报销跟踪表可更新字段（指针为 nil 表示「未提供、不改动」）。
//
// ★ 口径（FR-M6-07 同源）：集团侧字段只接受人工传入值，**不做任何派生 / 回填**。
type ExpenseTrackUpdate struct {
	ReviewState  *string
	HandoverDate *string
	PaidDate     *string
	PaidCents    *int64
	OverrunNote  *string
}

// UpdateExpenseTrack 更新集团报销跟踪表；返回是否命中记录。
func (d *DB) UpdateExpenseTrack(ctx context.Context, id int64, u ExpenseTrackUpdate) (bool, error) {
	set := make([]string, 0, 6)
	args := make([]any, 0, 7)
	appendStr := func(col string, v *string) {
		if v == nil {
			return
		}
		set = append(set, col+" = ?")
		args = append(args, nullStr(strings.TrimSpace(*v)))
	}
	appendStr("review_state", u.ReviewState)
	appendStr("handover_date", u.HandoverDate)
	appendStr("paid_date", u.PaidDate)
	appendStr("overrun_note", u.OverrunNote)
	if u.PaidCents != nil {
		set = append(set, "paid_cents = ?")
		args = append(args, *u.PaidCents)
	}
	if len(set) == 0 {
		return false, nil
	}
	set = append(set, "updated_at = ?")
	args = append(args, fmtTime(timeNow().UTC()))
	args = append(args, id)

	res, err := d.ExecContext(ctx, "UPDATE t_expense_track SET "+strings.Join(set, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return false, fmt.Errorf("store: 更新集团报销跟踪失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
