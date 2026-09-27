package store

import (
	"context"
	"database/sql"
	"errors"
)

// ---------- 单据编号器（t_doc_seq，04a §6） ----------

// NextDocSeqTx 在事务内对 (doc_type, yymm) **读改写**自增，返回新的序号（从 1 开始）。
//
// ★ 并发唯一（04a §6.2）：单实例 + SetMaxOpenConns(1) 使所有事务串行化，
// 写锁天然提供 `SELECT ... FOR UPDATE` 语义；调用方须把本方法放在**很短**的事务里
// （与 `t_instance.biz_no` 写入同事务，FR-M8-09）。
//
// ★ 锁号前提 P1（04a §6.4）：本表**只增不减**——除本方法外，任何地方都不得写小 last_seq、
// 不得删除本表行、不得把本表纳入归档清理。破坏即「终态单号被复用」，且**静默**。
func (d *DB) NextDocSeqTx(ctx context.Context, tx *sql.Tx, docType, yymm string) (int64, error) {
	var last int64
	err := tx.QueryRowContext(ctx,
		`SELECT last_seq FROM t_doc_seq WHERE doc_type = ? AND yymm = ?`, docType, yymm).Scan(&last)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		last = 0
	case err != nil:
		return 0, err
	}

	next := last + 1
	if _, err := tx.ExecContext(ctx, `
INSERT INTO t_doc_seq (doc_type, yymm, last_seq, updated_at) VALUES (?,?,?,?)
ON CONFLICT(doc_type, yymm) DO UPDATE SET
  last_seq = excluded.last_seq, updated_at = excluded.updated_at`,
		docType, yymm, next, fmtTime(timeNow().UTC())); err != nil {
		return 0, err
	}
	return next, nil
}

// PeekDocSeq 读取当前序号（只读）。found=false 表示该 (doc_type,yymm) 尚无游标。
//
// 供测试与巡检使用：可用于断言「游标单调不减」（锁号前提 P1 的可执行检查）。
func (d *DB) PeekDocSeq(ctx context.Context, docType, yymm string) (seq int64, found bool, err error) {
	err = d.QueryRowContext(ctx,
		`SELECT last_seq FROM t_doc_seq WHERE doc_type = ? AND yymm = ?`, docType, yymm).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	return seq, true, nil
}

// CountDocSeq 返回单据编号器的行数（巡检用：正常应随「doc_type × 月份」增长，绝不应归零）。
func (d *DB) CountDocSeq(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_doc_seq`).Scan(&n)
	return n, err
}
