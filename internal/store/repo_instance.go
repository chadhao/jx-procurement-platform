package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("store: 记录不存在")

// UpsertInstance 幂等写入实例主表（instance_code 唯一）；冲突时更新并保留 created_at。
// 空字符串列不覆盖已有非空值，避免事件报文缺字段时把已采集数据抹掉。
func (d *DB) UpsertInstance(ctx context.Context, in *Instance) error {
	return upsertInstance(ctx, d, in)
}

func upsertInstance(ctx context.Context, q execer, in *Instance) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO t_instance (
  instance_code, approval_code, doc_type, biz_no, biz_no_prefix, biz_no_yymm, biz_no_seq,
  status, status_raw, applicant_open_id, applicant_name, department, amount_cents,
  purpose_class_l1, purpose_class_l2, supplier, source, created_at, updated_at,
  update_time, prev_biz_no, cancel_reason, cancel_at, push_hash, push_at, ext_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(instance_code) DO UPDATE SET
  approval_code     = excluded.approval_code,
  doc_type          = COALESCE(NULLIF(excluded.doc_type,''), t_instance.doc_type),
  biz_no            = COALESCE(NULLIF(excluded.biz_no,''), t_instance.biz_no),
  biz_no_prefix     = COALESCE(NULLIF(excluded.biz_no_prefix,''), t_instance.biz_no_prefix),
  biz_no_yymm       = COALESCE(NULLIF(excluded.biz_no_yymm,''), t_instance.biz_no_yymm),
  biz_no_seq        = COALESCE(NULLIF(excluded.biz_no_seq,''), t_instance.biz_no_seq),
  -- ★ R18 守卫：**终态不得倒退**。库中已是终态（APPROVED/REJECTED/CANCELED）时，
  --   拒绝被改回任何非终态 —— 堵住「旧事件链 / 旧对账补拉把已终态覆盖回 PENDING」这一
  --   最危险的静默（界面看着正常、状态却退了，docs/11 R18/R23）。
  --   终态 → 终态（含撤回改判等未来场景）与 非终态 → 任意 均照常写入。
  status            = CASE
                        WHEN t_instance.status IN ('APPROVED','REJECTED','CANCELED')
                         AND excluded.status NOT IN ('APPROVED','REJECTED','CANCELED')
                        THEN t_instance.status
                        ELSE excluded.status
                      END,
  status_raw        = COALESCE(NULLIF(excluded.status_raw,''), t_instance.status_raw),
  -- ★ R17 write-once（docs/11 R17）：申请人身份与所属部门**一旦写入不得被覆盖**
  --   （旧语义「非空即覆盖」会让后到的报文中缺省/异构身份把已采集身份抹掉）。
  --   语义＝「库中已有非空值则保留，仅当其为空时才用新值填充」。
  applicant_open_id = COALESCE(NULLIF(t_instance.applicant_open_id,''), excluded.applicant_open_id),
  applicant_name    = COALESCE(NULLIF(t_instance.applicant_name,''), excluded.applicant_name),
  department        = COALESCE(NULLIF(t_instance.department,''), excluded.department),
  amount_cents      = COALESCE(excluded.amount_cents, t_instance.amount_cents),
  purpose_class_l1  = COALESCE(NULLIF(excluded.purpose_class_l1,''), t_instance.purpose_class_l1),
  purpose_class_l2  = COALESCE(NULLIF(excluded.purpose_class_l2,''), t_instance.purpose_class_l2),
  supplier          = COALESCE(NULLIF(excluded.supplier,''), t_instance.supplier),
  source            = excluded.source,
  updated_at        = excluded.updated_at,
  -- ★ update_time 单调递增（04a §3.1）：取两值较大者，**任何写入都不得使其回退**
  --   （版本回退会让飞书侧推送静默失败）。
  update_time       = MAX(COALESCE(t_instance.update_time,0), COALESCE(excluded.update_time,0)),
  prev_biz_no       = COALESCE(NULLIF(excluded.prev_biz_no,''), t_instance.prev_biz_no),
  cancel_reason     = COALESCE(NULLIF(excluded.cancel_reason,''), t_instance.cancel_reason),
  cancel_at         = COALESCE(NULLIF(excluded.cancel_at,''), t_instance.cancel_at),
  push_hash         = COALESCE(NULLIF(excluded.push_hash,''), t_instance.push_hash),
  push_at           = COALESCE(NULLIF(excluded.push_at,''), t_instance.push_at),
  -- ★ ext_json：空串不覆盖已有（防「落后快照把已构造的关联键抹成空」，同 R19）。
  ext_json          = COALESCE(NULLIF(excluded.ext_json,''), t_instance.ext_json)
`,
		in.InstanceCode, in.ApprovalCode, nullStr(in.DocType), nullStr(in.BizNo),
		nullStr(in.BizNoPrefix), nullStr(in.BizNoYYMM), nullStr(in.BizNoSeq),
		in.Status, nullStr(in.StatusRaw), nullStr(in.ApplicantOpenID), nullStr(in.ApplicantName),
		nullStr(in.Department), in.AmountCents, nullStr(in.PurposeClassL1), nullStr(in.PurposeClassL2),
		nullStr(in.Supplier), defaultStr(in.Source, "event"), fmtTime(in.CreatedAt), fmtTime(in.UpdatedAt),
		in.UpdateTime, nullStr(in.PrevBizNo), nullStr(in.CancelReason), nullStr(fmtMaybeTime(in.CancelAt)),
		nullStr(in.PushHash), nullStr(fmtMaybeTime(in.PushAt)), defaultStr(in.ExtJSON, "{}"),
	)
	if err != nil {
		return fmt.Errorf("store: 写入实例 %s 失败: %w", in.InstanceCode, err)
	}
	return nil
}

// GetInstance 按 instance_code 读取实例；不存在返回 ErrNotFound。
func (d *DB) GetInstance(ctx context.Context, instanceCode string) (*Instance, error) {
	row := d.QueryRowContext(ctx, instanceSelectSQL+` WHERE instance_code = ?`, instanceCode)
	return scanInstance(row)
}

// GetInstanceByBizNo 按业务单号读取实例（我方审批核心以 biz_no 为业务键）；不存在返回 ErrNotFound。
func (d *DB) GetInstanceByBizNo(ctx context.Context, bizNo string) (*Instance, error) {
	row := d.QueryRowContext(ctx, instanceSelectSQL+` WHERE biz_no = ?`, bizNo)
	return scanInstance(row)
}

// GetInstanceByBizNoTx 在事务内按业务单号读取实例。
func (d *DB) GetInstanceByBizNoTx(ctx context.Context, tx *sql.Tx, bizNo string) (*Instance, error) {
	row := tx.QueryRowContext(ctx, instanceSelectSQL+` WHERE biz_no = ?`, bizNo)
	return scanInstance(row)
}

// InstanceFilter 实例列表过滤条件（行级过滤由 RowSQL/RowArgs 注入）。
type InstanceFilter struct {
	ApprovalCode string
	DocType      string
	Status       string
	Department   string
	RowSQL       string
	RowArgs      []any
	Limit        int
	Offset       int
}

// ListInstances 查询实例列表；自动拼装行级过滤条件。
func (d *DB) ListInstances(ctx context.Context, f InstanceFilter) ([]Instance, int, error) {
	var (
		where []string
		args  []any
	)
	if f.ApprovalCode != "" {
		where = append(where, "approval_code = ?")
		args = append(args, f.ApprovalCode)
	}
	if f.DocType != "" {
		where = append(where, "doc_type = ?")
		args = append(args, f.DocType)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Department != "" {
		where = append(where, "department = ?")
		args = append(args, f.Department)
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
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_instance a`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := instanceSelectSQL + clause + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	rows, err := d.QueryContext(ctx, q, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []Instance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *inst)
	}
	return out, total, rows.Err()
}

// ExistingCodes 返回指定 approval_code 已入库的 instance_code 集合（对账求差用）。
func (d *DB) ExistingCodes(ctx context.Context, approvalCode string) (map[string]bool, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if approvalCode == "" {
		rows, err = d.QueryContext(ctx, `SELECT instance_code FROM t_instance`)
	} else {
		rows, err = d.QueryContext(ctx, `SELECT instance_code FROM t_instance WHERE approval_code = ?`, approvalCode)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		out[code] = true
	}
	return out, rows.Err()
}

// ---------- 实例表单字段 ----------

// UpsertFields 幂等写入实例表单字段（(instance_code, field_id) 唯一）。
func (d *DB) UpsertFields(ctx context.Context, instanceCode string, fields []InstanceField) error {
	return upsertFields(ctx, d, instanceCode, fields)
}

// UpsertFieldsTx 在事务内幂等写入实例表单字段。
func (d *DB) UpsertFieldsTx(ctx context.Context, tx *sql.Tx, instanceCode string, fields []InstanceField) error {
	return upsertFields(ctx, tx, instanceCode, fields)
}

func upsertFields(ctx context.Context, q execer, instanceCode string, fields []InstanceField) error {
	for i := range fields {
		f := fields[i]
		if _, err := q.ExecContext(ctx, `
INSERT INTO t_instance_field
  (instance_code, field_id, field_name, biz_field, value_text, value_type, raw_json, created_at)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(instance_code, field_id) DO UPDATE SET
  field_name = COALESCE(NULLIF(excluded.field_name,''), t_instance_field.field_name),
  biz_field  = COALESCE(NULLIF(excluded.biz_field,''), t_instance_field.biz_field),
  value_text = excluded.value_text,
  value_type = COALESCE(NULLIF(excluded.value_type,''), t_instance_field.value_type),
  raw_json   = COALESCE(NULLIF(excluded.raw_json,''), t_instance_field.raw_json)
`,
			instanceCode, f.FieldID, nullStr(f.FieldName), nullStr(f.BizField),
			nullStr(f.ValueText), nullStr(f.ValueType), nullStr(f.RawJSON), fmtTime(timeNow().UTC()),
		); err != nil {
			return fmt.Errorf("store: 写入字段 %s/%s 失败: %w", instanceCode, f.FieldID, err)
		}
	}
	return nil
}

// ListFields 读取实例表单字段（键值对，不依赖顺序）。
func (d *DB) ListFields(ctx context.Context, instanceCode string) ([]InstanceField, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id, instance_code, field_id, field_name, COALESCE(biz_field,''), value_text,
       COALESCE(value_type,''), COALESCE(raw_json,''), created_at
FROM t_instance_field WHERE instance_code = ? ORDER BY id`, instanceCode)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []InstanceField
	for rows.Next() {
		var f InstanceField
		var created string
		var valueText sql.NullString
		if err := rows.Scan(&f.ID, &f.InstanceCode, &f.FieldID, &f.FieldName, &f.BizField,
			&valueText, &f.ValueType, &f.RawJSON, &created); err != nil {
			return nil, err
		}
		f.ValueText = valueText.String
		f.CreatedAt = parseTime(created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---------- 状态变更史（追加式） ----------

// NextEventSeq 计算某实例下一个 event_seq（MAX+1）。须在事务内调用以保证递增。
func nextEventSeq(ctx context.Context, q execer, instanceCode string) (int64, error) {
	var seq int64
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(event_seq),0)+1 FROM t_instance_status_history WHERE instance_code = ?`,
		instanceCode).Scan(&seq)
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// AppendHistory 追加一条状态变更史（永不覆盖历史，天然满足 TC-16）。
func appendHistory(ctx context.Context, q execer, h *StatusHistory) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO t_instance_status_history
  (instance_code, status, task_node, operator_open_id, opinion, occurred_at, event_seq, created_at)
VALUES (?,?,?,?,?,?,?,?)`,
		h.InstanceCode, h.Status, nullStr(h.TaskNode), nullStr(h.OperatorOpenID),
		nullStr(h.Opinion), fmtTime(h.OccurredAt), h.EventSeq, fmtTime(timeNow().UTC()),
	)
	if err != nil {
		return fmt.Errorf("store: 追加状态史 %s 失败: %w", h.InstanceCode, err)
	}
	return nil
}

// ListHistory 读取实例状态变更史（按 event_seq 升序）。
func (d *DB) ListHistory(ctx context.Context, instanceCode string) ([]StatusHistory, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id, instance_code, status, COALESCE(task_node,''), COALESCE(operator_open_id,''),
       COALESCE(opinion,''), occurred_at, event_seq, created_at
FROM t_instance_status_history WHERE instance_code = ? ORDER BY event_seq`, instanceCode)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []StatusHistory
	for rows.Next() {
		var h StatusHistory
		var occurred, created string
		if err := rows.Scan(&h.ID, &h.InstanceCode, &h.Status, &h.TaskNode, &h.OperatorOpenID,
			&h.Opinion, &occurred, &h.EventSeq, &created); err != nil {
			return nil, err
		}
		h.OccurredAt = parseTime(occurred)
		h.CreatedAt = parseTime(created)
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---------- 扫描辅助 ----------

// instanceSelectSQL ★ 0007 增列（update_time/prev_biz_no/cancel_reason/cancel_at/push_hash/push_at）
// 追加在末尾（只加列，不改既有列顺序与语义）。
const instanceSelectSQL = `
SELECT id, instance_code, approval_code, COALESCE(doc_type,''), COALESCE(biz_no,''),
       COALESCE(biz_no_prefix,''), COALESCE(biz_no_yymm,''), COALESCE(biz_no_seq,''),
       status, COALESCE(status_raw,''), COALESCE(applicant_open_id,''), COALESCE(applicant_name,''),
       COALESCE(department,''), amount_cents, COALESCE(purpose_class_l1,''), COALESCE(purpose_class_l2,''),
       COALESCE(supplier,''), source, created_at, updated_at,
       COALESCE(update_time,0), COALESCE(prev_biz_no,''), COALESCE(cancel_reason,''),
       COALESCE(cancel_at,''), COALESCE(push_hash,''), COALESCE(push_at,''),
       COALESCE(ext_json,'{}')
FROM t_instance a`

func scanInstance(s interface {
	Scan(dest ...any) error
}) (*Instance, error) {
	var (
		in      Instance
		amount  sql.NullInt64
		created string
		updated string
	)
	var (
		cancelAt string
		pushAt   string
	)
	if err := s.Scan(&in.ID, &in.InstanceCode, &in.ApprovalCode, &in.DocType, &in.BizNo,
		&in.BizNoPrefix, &in.BizNoYYMM, &in.BizNoSeq, &in.Status, &in.StatusRaw,
		&in.ApplicantOpenID, &in.ApplicantName, &in.Department, &amount,
		&in.PurposeClassL1, &in.PurposeClassL2, &in.Supplier, &in.Source, &created, &updated,
		&in.UpdateTime, &in.PrevBizNo, &in.CancelReason, &cancelAt, &in.PushHash, &pushAt, &in.ExtJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if amount.Valid {
		v := amount.Int64
		in.AmountCents = &v
	}
	in.CreatedAt = parseTime(created)
	in.UpdatedAt = parseTime(updated)
	in.CancelAt = parseTimePtr(cancelAt)
	in.PushAt = parseTimePtr(pushAt)
	return &in, nil
}

// GetInstanceTx 在事务内按 instance_code 读取实例。
func (d *DB) GetInstanceTx(ctx context.Context, tx *sql.Tx, instanceCode string) (*Instance, error) {
	row := tx.QueryRowContext(ctx, instanceSelectSQL+` WHERE instance_code = ?`, instanceCode)
	return scanInstance(row)
}

// UpsertInstanceTx 在事务内幂等写入实例主表。
func (d *DB) UpsertInstanceTx(ctx context.Context, tx *sql.Tx, in *Instance) error {
	return upsertInstance(ctx, tx, in)
}

// AppendStatusHistory 在事务内追加一条状态变更史（自动分配递增 event_seq）。
// 追加式写入，永不覆盖历史，天然满足「驳回重提原单留痕」（TC-16）。
//
// ★ B46（架构审查：`inbox` 对 `approval_instance` 与 `approval_task` **一视同仁**）：
// 每个节点事件都会走到这里；而本表**没有唯一约束**、`appendHistory` 又是纯 INSERT →
// 同一状态被反复追加，**状态历史堆满重复行、时间线变噪声**（该表直接不可用）。
//
// 故做**同状态去重**：与最近一行 (status, operator, opinion) **三者全同**则跳过，
// 且**不消耗 event_seq**（序号保持稀疏无空洞）。用「三者全同」而非「仅 status 相同」，
// 是为了不误杀有意义的变化（换人、带新意见）。
//
// 返回：seq（跳过时为最近一行的序号）、appended（本次是否真的追加）。
func (d *DB) AppendStatusHistory(ctx context.Context, tx *sql.Tx, h *StatusHistory) (int64, bool, error) {
	prevStatus, prevOp, prevOpinion, prevSeq, found, err := latestHistory(ctx, tx, h.InstanceCode)
	if err != nil {
		return 0, false, err
	}
	if found && prevStatus == h.Status &&
		strings.TrimSpace(prevOp) == strings.TrimSpace(h.OperatorOpenID) &&
		strings.TrimSpace(prevOpinion) == strings.TrimSpace(h.Opinion) {
		return prevSeq, false, nil
	}
	seq, err := nextEventSeq(ctx, tx, h.InstanceCode)
	if err != nil {
		return 0, false, err
	}
	h.EventSeq = seq
	if err := appendHistory(ctx, tx, h); err != nil {
		return 0, false, err
	}
	return seq, true, nil
}

// latestHistory 读取某实例**最近一行**状态史（去重比对用）；无行时 found=false。
func latestHistory(ctx context.Context, q execer, instanceCode string) (
	status, operator, opinion string, seq int64, found bool, err error) {
	row := q.QueryRowContext(ctx, `
SELECT status, COALESCE(operator_open_id,''), COALESCE(opinion,''), event_seq
FROM t_instance_status_history WHERE instance_code = ?
ORDER BY event_seq DESC LIMIT 1`, instanceCode)
	switch err = row.Scan(&status, &operator, &opinion, &seq); {
	case err == sql.ErrNoRows:
		return "", "", "", 0, false, nil
	case err != nil:
		return "", "", "", 0, false, err
	}
	return status, operator, opinion, seq, true, nil
}

// UpsertArchiveTx 在事务内幂等写台账存档。
func (d *DB) UpsertArchiveTx(ctx context.Context, tx *sql.Tx, a *LedgerArchive) error {
	return upsertArchive(ctx, tx, a)
}

// nullStr 将空字符串转为 NULL，避免唯一约束/COALESCE 语义歧义。
func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// defaultStr 返回 s，为空时返回 d。
func defaultStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
