// Package number 生成我方业务单号（`{前缀}-{YYMM}-{####}`，04a §6 / 01a §3）。
//
// ★ 为什么单号必须由我方生成：三方审批定义**不含流水号控件**（F3），
//
//	原「飞书流水号控件生成、自建侧只读」的口径在转向后字面为假。
//
// ★ 锁号（终态永久不复用）的实现依托（04a §6.4 三前提）：
//
//	P1 `t_doc_seq` 单调递增、只增不减、不参与归档/清理；
//	P2 实例永不硬删除（撤回＝CANCELED 仍占号）；
//	P3 `t_instance.biz_no` 的 UNIQUE 兜底。
//	三者任一被破坏 → 单号可被复用，且**失败是静默的**（台账/审计出现「同号两笔」）。
package number

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// cst 业务本地时区＝Asia/Shanghai（全年 UTC+8，无夏令时）。
//
// ★ 用固定偏移而非 time.LoadLocation("Asia/Shanghai")：避免对系统 tzdata 的依赖
// （Windows/精简容器常缺时区库），而结果与 Asia/Shanghai 完全一致（无 DST）。
var cst = time.FixedZone("CST", 8*3600)

// maxSeqPerMonth 月度序号上界（`####` 为 4 位十进制）。
//
// ★ 超过则不静默产出 5 位号（那会让「格式契约」与台账/检索口径悄悄漂移），
//
//	而是**显式报错**，交人工处置（与 04a §10 静默防护同族）。
const maxSeqPerMonth = 9999

// ErrOverflow 当月序号超出 4 位上限。
var ErrOverflow = errors.New("number: 当月单号序号超上限（>9999）")

// NumberKey 返回单据类型的**号段归并键**。
//
// ★ 为什么需要归并：前缀与号段必须一一对应，否则「同前缀不同号段」会**撞号**——
//
//	例：PO（采购订单）与 CT 同用 `CT` 前缀（04a §6.1「PO 沿用 CT」），
//	若各自占一个号段，二者都会生成 `CT-YYMM-0001` → 同号两笔，锁号失效。
//	故 PO 归并回 CT，共用同一 (doc_type,yymm) 游标。
func NumberKey(docType string) string {
	d := strings.ToUpper(strings.TrimSpace(docType))
	switch d {
	case "PO":
		return "CT"
	default:
		return d
	}
}

// YYMM 返回业务本地年月（YYMM，如 2609）。
func YYMM(at time.Time) string { return at.In(cst).Format("0601") }

// Generator 单号生成器（依赖 store 的 t_doc_seq 读改写）。
type Generator struct {
	db *store.DB
}

// New 构造生成器。
func New(db *store.DB) *Generator { return &Generator{db: db} }

// AllocTx 在**调用方事务**内分配一个新单号并推进 `t_doc_seq`。
//
// ★ 必须与 `t_instance.biz_no` 写入同事务（04a §6.1「生成时机」），
//
//	否则「分配了号但实例没落库」会在游标上留下空洞（可接受），
//	而「实例落库但未推进游标」会导致重号（不可接受）。
func (g *Generator) AllocTx(ctx context.Context, tx *sql.Tx, docType string, at time.Time) (string, error) {
	key := NumberKey(docType)
	if key == "" {
		return "", fmt.Errorf("number: 单据类型为空，无法生成单号")
	}
	if at.IsZero() {
		at = time.Now()
	}
	yymm := YYMM(at)

	seq, err := g.db.NextDocSeqTx(ctx, tx, key, yymm)
	if err != nil {
		return "", fmt.Errorf("number: 推进单号游标失败: %w", err)
	}
	if seq > maxSeqPerMonth {
		return "", fmt.Errorf("%w（doc_type=%s yymm=%s seq=%d）", ErrOverflow, key, yymm, seq)
	}
	return fmt.Sprintf("%s-%s-%04d", key, yymm, seq), nil
}

// Alloc 以独立事务分配一个单号（便于测试与一次性调用）。
func (g *Generator) Alloc(ctx context.Context, docType string, at time.Time) (string, error) {
	var bizNo string
	err := g.db.WithTx(ctx, func(tx *sql.Tx) error {
		var e error
		bizNo, e = g.AllocTx(ctx, tx, docType, at)
		return e
	})
	if err != nil {
		return "", err
	}
	return bizNo, nil
}
