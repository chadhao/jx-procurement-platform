package store

// 审批提交幂等（M4，d9：照抄 submission「事务内占位 + 指纹 + 40900」模式）。
//
// ★ 为什么必须在事务内：连接池仅 1 条连接，但池只串行化**单条语句**、
//   不串行化语句序列 —— 「先查后写」两条独立语句仍可被并发请求交错（TOCTOU）。
//   事务让整段序列独占连接；0015 的部分唯一索引再做与并发无关的兜底。
// ★ 语句顺序：**必须先占位幂等键，再建单**。若反过来，同键同载荷的重试会
//   先撞业务唯一约束，被误判为「单号已存在」→ 409 而非 200 复用首次结果。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrApprovalIdemTaken 幂等键已被占用（唯一约束冲突）——调用方让路后回读胜出者。
var ErrApprovalIdemTaken = errors.New("store: 审批提交幂等键已被占用")

// ApprovalIdemRecord 胜出者的幂等记录。
type ApprovalIdemRecord struct {
	BizNo       string
	PayloadHash string
}

// isUniqueErr 判断是否 SQLite 唯一约束冲突。
func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "CONSTRAINT FAILED")
}

// InsertApprovalIdemTx 在事务内占位幂等键（detail_json 先写 biz_no="" 占位，
// 建单成功后经 BackfillApprovalIdemTx 回填）。
func (d *DB) InsertApprovalIdemTx(ctx context.Context, tx *sql.Tx, key, actor, payloadHash string) error {
	placeholder, _ := json.Marshal(map[string]any{"biz_no": "", "payload_hash": payloadHash})
	_, err := tx.ExecContext(ctx, `
INSERT INTO t_audit_log (actor_open_id, action, resource, target_id, result, detail_json, created_at)
VALUES (?, 'approval_submit_idem', 'approval', ?, 'allow', ?, ?)`,
		actor, key, string(placeholder), fmtTime(time.Now()))
	if err != nil {
		if isUniqueErr(err) {
			return ErrApprovalIdemTaken
		}
		return fmt.Errorf("store: 占位审批幂等键失败: %w", err)
	}
	return nil
}

// BackfillApprovalIdemTx 建单成功后回填 biz_no（仍在同一事务内）；
// payloadHash 一并写回（占位时写过，回填不许丢 —— 重放比对依赖它）。
func (d *DB) BackfillApprovalIdemTx(ctx context.Context, tx *sql.Tx, key, actor, bizNo, payloadHash string) error {
	detail, _ := json.Marshal(map[string]any{"biz_no": bizNo, "payload_hash": payloadHash})
	res, err := tx.ExecContext(ctx, `
UPDATE t_audit_log SET detail_json = ?
WHERE action = 'approval_submit_idem' AND target_id = ? AND actor_open_id = ?`,
		string(detail), key, actor)
	if err != nil {
		return fmt.Errorf("store: 回填审批幂等键失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 理论不可达（同事务刚占位）；不静默 —— 让提交失败好过幂等簿记缺失。
		return fmt.Errorf("store: 回填审批幂等键未命中行（key=%s）", key)
	}
	return nil
}

// FindApprovalIdem 读取胜出者的幂等记录（占位冲突后、事务外调用）。
// ★ 保留 payload_hash（首次回填时会写入；见 handler 侧 Backfill 参数化需求）。
func (d *DB) FindApprovalIdem(ctx context.Context, key, actor string) (ApprovalIdemRecord, bool, error) {
	var detail sql.NullString
	err := d.QueryRowContext(ctx, `
SELECT detail_json FROM t_audit_log
WHERE action = 'approval_submit_idem' AND target_id = ? AND actor_open_id = ?
ORDER BY id DESC LIMIT 1`, key, actor).Scan(&detail)
	if errors.Is(err, sql.ErrNoRows) {
		return ApprovalIdemRecord{}, false, nil
	}
	if err != nil {
		return ApprovalIdemRecord{}, false, fmt.Errorf("store: 查询审批幂等键失败: %w", err)
	}
	rec := ApprovalIdemRecord{}
	if detail.Valid && detail.String != "" {
		var payload struct {
			BizNo       string `json:"biz_no"`
			PayloadHash string `json:"payload_hash"`
		}
		if err := json.Unmarshal([]byte(detail.String), &payload); err != nil {
			// fail-closed：记录损坏绝不当作「同载荷可复用」。
			return ApprovalIdemRecord{}, false, fmt.Errorf("store: 审批幂等键记录损坏（key=%s）: %w", key, err)
		}
		rec.BizNo = payload.BizNo
		rec.PayloadHash = payload.PayloadHash
	}
	return rec, true, nil
}
