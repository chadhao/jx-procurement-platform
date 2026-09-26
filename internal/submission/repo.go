package submission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ErrConflict 表示唯一约束冲突（重复账期 / 业务单号重复 → 接口层映射为 40900）。
var ErrConflict = errors.New("submission: 唯一约束冲突")

// CloseInfo 备付金月核销记录（余额只读展示的锚点，FR-M1-07）。
type CloseInfo struct {
	Period       string
	IssuedCents  int64
	SpentCents   int64
	BalanceCents int64
	Remark       string
	AsOf         time.Time
}

// Repo 直接访问 M1 备付金 / M6 报送相关表。
//
// 说明：存储层已有 CreateSubmission / GetSubmission / ListSubmissions / InsertSubmissionItem /
// ListSubmissionItems / InsertPettyCashReceipt / UpsertPettyCashClose / GetPettyCashBalance 等可复用函数，
// 本文件只补充「存储层缺失、且必须新增」的读写（更新报送字段、账期唯一写入、幂等键登记、
// 余额三段式读取、无上限列表），避免修改既有存储文件造成并行冲突。
type Repo struct{ db *store.DB }

// NewRepo 构造仓储。
func NewRepo(db *store.DB) *Repo { return &Repo{db: db} }

// ---------- M1 备付金 ----------

// InsertPettyCashClose 按月核销写入「唯一账期一条」记录；
// 命中 UNIQUE(period) 时返回 ErrConflict（重复核销 → 40900）。
//
// ★ 口径：备付金不定额、以实有金额为限；按月凭单据核销，balance_cents 为核销后实有金额锚点。
func (r *Repo) InsertPettyCashClose(ctx context.Context, period string, issued, spent, balance int64, remark, createdBy string) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO t_petty_cash_close (period, issued_cents, spent_cents, balance_cents, remark, created_by, created_at)
VALUES (?,?,?,?,?,?,?)`,
		period, issued, spent, balance, nullStr(remark), nullStr(createdBy), fmtTime(time.Now()))
	if err != nil {
		if IsUniqueError(err) {
			return ErrConflict
		}
		return fmt.Errorf("submission: 写备付金月核销失败: %w", err)
	}
	return nil
}

// PettyCashClose 读取某账期核销记录；不存在返回 (nil,false,nil)。
func (r *Repo) PettyCashClose(ctx context.Context, period string) (*CloseInfo, bool, error) {
	var (
		info    CloseInfo
		issued  sql.NullInt64
		spent   sql.NullInt64
		balance sql.NullInt64
		remark  sql.NullString
		created sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
SELECT period, issued_cents, spent_cents, balance_cents, remark, created_at
FROM t_petty_cash_close WHERE period = ?`, period).
		Scan(&info.Period, &issued, &spent, &balance, &remark, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("submission: 读备付金月核销失败: %w", err)
	}
	info.IssuedCents = issued.Int64
	info.SpentCents = spent.Int64
	info.BalanceCents = balance.Int64
	info.Remark = remark.String
	if created.Valid {
		info.AsOf = parseTimeOrZero(created.String)
	}
	return &info, true, nil
}

// ---------- M6 报送 ----------

// ListAllSubmissions 查询报送登记（无 200 上限），供接口层做「超期过滤 + 内存分页」。
//
// 数据量口径为「数百次 / 月量级」（FR-M0-08），全量拉取 + 内存过滤可保证 overdue 判定与
// 分页一致（overdue 为派生字段，无法在纯 SQL 分页中正确统计 total）。
func (r *Repo) ListAllSubmissions(ctx context.Context, state, period string) ([]store.Submission, error) {
	var (
		where []string
		args  []any
	)
	if strings.TrimSpace(state) != "" {
		where = append(where, "submit_state = ?")
		args = append(args, strings.TrimSpace(state))
	}
	if strings.TrimSpace(period) != "" {
		where = append(where, "submit_date LIKE ?")
		args = append(args, strings.TrimSpace(period)+"%")
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, COALESCE(biz_no,''), COALESCE(subject_type,''), amount_cents, COALESCE(pay_method,''),
       COALESCE(hn_finish_date,''), COALESCE(submit_date,''), COALESCE(receipt_ref,''), submit_state,
       COALESCE(grp_accept_no,''), COALESCE(grp_state,''), COALESCE(paid_date,''), COALESCE(reject_reason,''),
       COALESCE(department,''), COALESCE(applicant_open_id,''), COALESCE(assigned_open_id,''), COALESCE(acceptors,''),
       COALESCE(created_by,''), created_at, updated_at
FROM t_submission`+clause+` ORDER BY id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("submission: 查询报送列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.Submission
	for rows.Next() {
		var (
			s       store.Submission
			amount  sql.NullInt64
			created string
			updated string
		)
		if err := rows.Scan(&s.ID, &s.BizNo, &s.SubjectType, &amount, &s.PayMethod, &s.HNFinishDate,
			&s.SubmitDate, &s.ReceiptRef, &s.SubmitState, &s.GrpAcceptNo, &s.GrpState, &s.PaidDate,
			&s.RejectReason, &s.Department, &s.ApplicantOpenID, &s.AssignedOpenID, &s.Acceptors,
			&s.CreatedBy, &created, &updated); err != nil {
			return nil, err
		}
		if amount.Valid {
			v := amount.Int64
			s.AmountCents = &v
		}
		s.CreatedAt = parseTimeOrZero(created)
		s.UpdatedAt = parseTimeOrZero(updated)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ApplyReceipt 登记 / 补填移交凭证（可同时更新报送状态）；返回是否命中记录。
func (r *Repo) ApplyReceipt(ctx context.Context, id int64, receiptRef, submitState string) (bool, error) {
	return r.update(ctx, id, map[string]string{
		"receipt_ref":  receiptRef,
		"submit_state": submitState,
	})
}

// ApplyGroup 人工登记集团侧字段（受理编号 / 流程状态 / 付款完成日期）与状态。
//
// ★ FR-M6-07：仅接受人工传入值，不做任何派生 / 回填；空值字段不覆盖原有值。
func (r *Repo) ApplyGroup(ctx context.Context, id int64, acceptNo, grpState, paidDate, submitState string) (bool, error) {
	fields := map[string]string{}
	if strings.TrimSpace(acceptNo) != "" {
		fields["grp_accept_no"] = acceptNo
	}
	if strings.TrimSpace(grpState) != "" {
		fields["grp_state"] = grpState
	}
	if strings.TrimSpace(paidDate) != "" {
		fields["paid_date"] = paidDate
	}
	if strings.TrimSpace(submitState) != "" {
		fields["submit_state"] = submitState
	}
	return r.update(ctx, id, fields)
}

// ApplyReject 登记集团驳回处置（原因 + 状态 已驳回）；返回是否命中记录。
func (r *Repo) ApplyReject(ctx context.Context, id int64, rejectReason, submitState string) (bool, error) {
	return r.update(ctx, id, map[string]string{
		"reject_reason": rejectReason,
		"submit_state":  submitState,
	})
}

// submissionColumnWhitelist 允许更新的列白名单（防注入）。
var submissionColumnWhitelist = map[string]bool{
	"receipt_ref":   true,
	"submit_state":  true,
	"grp_accept_no": true,
	"grp_state":     true,
	"paid_date":     true,
	"reject_reason": true,
}

func (r *Repo) update(ctx context.Context, id int64, fields map[string]string) (bool, error) {
	set := make([]string, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+2)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !submissionColumnWhitelist[k] {
			return false, fmt.Errorf("submission: 非法列名 %q", k)
		}
		set = append(set, k+" = ?")
		args = append(args, fields[k])
	}
	set = append(set, "updated_at = ?")
	args = append(args, fmtTime(time.Now()))
	args = append(args, id)

	res, err := r.db.ExecContext(ctx, "UPDATE t_submission SET "+strings.Join(set, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return false, fmt.Errorf("submission: 更新报送登记失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ---------- 幂等键登记（报送登记支持 Idempotency-Key） ----------

// 幂等闭环的哨兵错误（接口层据此映射 HTTP 状态码）。
var (
	// ErrDuplicateBizNo 业务单号已存在 → 40900。
	ErrDuplicateBizNo = errors.New("submission: 业务单号已存在")
	// ErrIdemReplay 同键 + 同载荷命中 → 调用方应 200 复用首次登记结果。
	ErrIdemReplay = errors.New("submission: 幂等键命中，应复用首次登记结果")
	// ErrIdemConflict 同键 + 异载荷冲突 → 调用方应 40900。
	ErrIdemConflict = errors.New("submission: 幂等键冲突（同一键用于不同载荷）")
)

// IdemRecord 幂等键登记记录：首次登记产生的报送 id + 当时请求载荷的指纹。
//
// PayloadHash 为历史遗留行可能为空（在引入载荷比对前登记的键）：此时无法判定载荷是否一致，
// 处理器按「保守冲突」处理（见 handlers_submission.go）。
type IdemRecord struct {
	SubmissionID int64
	PayloadHash  string
}

// FindIdem 查找「同一调用方」已登记的幂等键；不存在返回 (nil,false,nil)。
//
// ★ 必须带 actor：幂等键**按调用方隔离**（与 0002 迁移的部分唯一索引双列一致）。
// 否则甲方使用的键被乙方复用时，乙方会读到甲方的报送记录（信息泄漏）。
func (r *Repo) FindIdem(ctx context.Context, key, actor string) (*IdemRecord, bool, error) {
	var detail sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT detail_json FROM t_audit_log
WHERE action = 'submission_idem' AND target_id = ? AND actor_open_id = ?
ORDER BY id DESC LIMIT 1`, key, actor).Scan(&detail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("submission: 查询幂等键失败: %w", err)
	}
	rec := &IdemRecord{}
	if detail.Valid {
		var payload struct {
			SubmissionID int64  `json:"submission_id"`
			PayloadHash  string `json:"payload_hash"`
		}
		_ = json.Unmarshal([]byte(detail.String), &payload)
		rec.SubmissionID = payload.SubmissionID
		rec.PayloadHash = payload.PayloadHash
	}
	return rec, true, nil
}

// CreateWithIdem 在**单个事务**内完成「建报送 + 建关联项 + 占位幂等键」，消除先查后写竞态。
//
// 返回：
//   - (id, 0, nil)                   新建成功
//   - (0, existingID, ErrIdemReplay) 同键 + 同载荷 → 调用方应 200 复用既有记录
//   - (0, existingID, ErrIdemConflict) 同键 + 异载荷 → 调用方应 40900
//   - (0, 0, ErrDuplicateBizNo)      业务单号重复 → 40900
//
// ★ 为什么必须在事务内：原实现为「FindIdem → CreateSubmission → RecordIdem」三条独立语句。
// 连接池虽限制为 1 条连接（store.Open 的 SetMaxOpenConns(1)），但池只串行化**单条语句**、
// **不串行化语句序列** —— 两条并发请求仍可交错通过各自的 FindIdem，然后各建一条记录（TOCTOU）。
// 事务让整段序列独占连接；0002 迁移的部分唯一索引再提供一道**与并发模型无关**的兜底。
//
// ★★ 语句顺序至关重要：**必须先占位幂等键，再建报送**。
// 若反过来（先建报送再占位），「同键同载荷」的重试会先撞上 `t_submission.biz_no` 的唯一约束，
// 于是被误判为「业务单号已存在」→ 返回 409 而不是 200 复用首次结果 ——
// 语义退化：调用方把「重试成功」当成「冲突」。
//
// ★ 事务内失败一律整体回滚：否则「幂等键已占位但报送没建成」会永久毒化该键，
// 客户端拿同一键重试永远拿不到成功结果。
func (r *Repo) CreateWithIdem(ctx context.Context, s *store.Submission, items []store.SubmissionItem,
	key, payloadHash, actor string) (int64, int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("submission: 开启事务失败: %w", err)
	}
	// 提交成功后 Rollback 是 no-op，故可无条件 defer。
	defer func() { _ = tx.Rollback() }()
	now := fmtTime(time.Now())

	// 1) ★ 先占位幂等键。唯一约束冲突 ⇒ 已有并发/先前的胜出者，本次一律让路。
	//    detail_json 先写 submission_id=0 占位，待报送建好后回填（见第 4 步）。
	var idemRowID int64
	if strings.TrimSpace(key) != "" {
		placeholder, _ := json.Marshal(map[string]any{
			"submission_id": 0,
			"payload_hash":  payloadHash,
		})
		res, err := tx.ExecContext(ctx, `
INSERT INTO t_audit_log (actor_open_id, action, resource, target_id, result, detail_json, created_at)
VALUES (?, 'submission_idem', 'submission', ?, 'allow', ?, ?)`,
			actor, key, string(placeholder), now)
		if err != nil {
			if !IsUniqueError(err) {
				return 0, 0, fmt.Errorf("submission: 占位幂等键失败: %w", err)
			}
			// 让路并回滚（本次未写入任何业务数据），再读胜出者的记录。
			_ = tx.Rollback()
			rec, found, ferr := r.FindIdem(ctx, key, actor)
			if ferr != nil {
				return 0, 0, ferr
			}
			if !found {
				// 理论不可达：刚判定冲突却查不到记录。
				return 0, 0, ErrIdemConflict
			}
			if rec.PayloadHash != "" && rec.PayloadHash == payloadHash {
				return 0, rec.SubmissionID, ErrIdemReplay
			}
			return 0, rec.SubmissionID, ErrIdemConflict
		}
		if idemRowID, err = res.LastInsertId(); err != nil {
			return 0, 0, fmt.Errorf("submission: 读取幂等键行 id 失败: %w", err)
		}
	}

	// 2) 建报送登记。
	res, err := tx.ExecContext(ctx, `
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
		nullStr(s.CreatedBy), now, now)
	if err != nil {
		if IsUniqueError(err) {
			// 走到这里说明业务单号确已被**另一次登记**占用（幂等键路径已在第 1 步处理）。
			return 0, 0, ErrDuplicateBizNo
		}
		return 0, 0, fmt.Errorf("submission: 新建报送登记失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, fmt.Errorf("submission: 读取新建报送 id 失败: %w", err)
	}

	// 3) 关联单据清单。
	for _, it := range items {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO t_submission_item (submission_id, item_biz_no, item_type) VALUES (?,?,?)`,
			id, it.ItemBizNo, nullStr(it.ItemType)); err != nil {
			return 0, 0, fmt.Errorf("submission: 写关联单据失败: %w", err)
		}
	}

	// 4) 回填幂等键的 detail_json（提交前同事务内完成，保证「键 ↔ 报送」要么都成立要么都不成立）。
	if idemRowID > 0 {
		detail, _ := json.Marshal(map[string]any{
			"submission_id": id,
			"payload_hash":  payloadHash,
		})
		if _, err := tx.ExecContext(ctx,
			`UPDATE t_audit_log SET detail_json = ? WHERE id = ?`, string(detail), idemRowID); err != nil {
			return 0, 0, fmt.Errorf("submission: 回填幂等键失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("submission: 提交事务失败: %w", err)
	}
	return id, 0, nil
}

// ---------- 小工具 ----------

// IsUniqueError 判断错误是否为 SQLite 唯一约束冲突。
func IsUniqueError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "CONSTRAINT FAILED")
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func parseTimeOrZero(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
