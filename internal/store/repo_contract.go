package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ---------- M4 变更链回溯（FR-M4-07） ----------

// GetArchiveByKey 按 (ledger_type, biz_no) 读取台账存档行（业务主键贯穿，不做二次录入）。
func (d *DB) GetArchiveByKey(ctx context.Context, ledgerType, bizNo string) (*LedgerArchive, error) {
	row := d.QueryRowContext(ctx, archiveSelectSQL+` WHERE ledger_type = ? AND biz_no = ?`, ledgerType, bizNo)
	return scanArchive(row)
}

// ListChangesByContract 按合同号回溯历次变更（FR-M4-07，制度第五十二条）。
//
// 数据源：例外事项台账（L09，含独家 / 紧急 / 变更三种例外）中与合同号相关的行。
//
// ★ Q14 定稿（2026-09-26）后匹配方式：**单一键名精确匹配** —— `ext_json.contract_no`。
//
//	原实现用 `json_each(ext_json)` 对**任意键的值**做等值比较（键名无关），那是 Q14 未定稿时的
//	兼容做法；键名一旦成为契约，就不该再"猜任意键"——那会把某个恰好等于合同号的无关值也拉进来
//	（例如某行把 `original_biz_no` 填成了合同号）。故收敛为 `json_extract(ext_json,'$.contract_no')`。
//
// ★ `json_valid` 守卫保留：SQLite 的 `json_extract` 遇非法 JSON 同样会抛错使**整条查询失败**，
//
//	故对脏行先判合法性（与 §H.10 P0-B 同一处理）。
//
// 行级权限由 rowSQL/rowArgs 在 SQL 层注入（表别名为 a）。
func (d *DB) ListChangesByContract(ctx context.Context, contractNo, rowSQL string, rowArgs []any) ([]LedgerArchive, error) {
	contractNo = strings.TrimSpace(contractNo)
	if contractNo == "" {
		return nil, nil
	}
	return d.ListArchiveByExtKey(ctx, "L09", KeyContractNo, contractNo, rowSQL, rowArgs)
}

// ExtKey 已登记的「跨单据关联键」——收敛后的**唯一两种**关联键名（Q14 定稿）。
type ExtKey string

const (
	// KeyContractNo 合同链：变更单 / 补充约定 → 原合同号。
	KeyContractNo ExtKey = "contract_no"
	// KeyRelatedBizNo 其他上下游关系：QC 来料检验报告 → 其 GR 入库单；GR → 其 CT 合同号。
	KeyRelatedBizNo ExtKey = "related_biz_no"
)

// jsonPath 返回 SQLite JSON 路径（键名来自本包常量，不接受外部输入）。
func (k ExtKey) jsonPath() string { return "$." + string(k) }

// ListArchiveByExtKey 按 `ext_json` 中**指定键**的等值匹配，查询同台账下的存档行。
//
// 用途：跨单据关联的查询侧实现——
//
//	L07 到货验收台账：查某 GR 行关联的 QC 行（`related_biz_no = GR 单号`）→ 取其检验结论；
//	L09 例外事项台账：查某合同号下的全部变更（`contract_no = 合同号`）。
//
// ★ 与 `ListChangesByContract` 的旧实现相比，这里**只认一个键**，不做任意键扫描。
// ★ `biz_no = ?` 一并命中，保留「行本身就是目标单号」这一义（如直接传某单据号）。
// ★ `json_valid` 守卫：脏 JSON 行不参与比较，但不得使整条查询失败。
func (d *DB) ListArchiveByExtKey(
	ctx context.Context, ledgerType string, key ExtKey, value, rowSQL string, rowArgs []any,
) ([]LedgerArchive, error) {
	value = strings.TrimSpace(value)
	if ledgerType == "" || key == "" || value == "" {
		return nil, nil
	}
	where := []string{
		"a.ledger_type = ?",
		"(a.biz_no = ? OR (CASE WHEN json_valid(a.ext_json) THEN json_extract(a.ext_json, ?) END = ?))",
	}
	args := []any{ledgerType, value, key.jsonPath(), value}
	if strings.TrimSpace(rowSQL) != "" {
		where = append(where, rowSQL)
		args = append(args, rowArgs...)
	}
	q := archiveSelectSQL + " WHERE " + strings.Join(where, " AND ") + " ORDER BY a.id ASC"

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 按 %s 关联查询台账失败: %w", key, err)
	}
	defer func() { _ = rows.Close() }()

	var out []LedgerArchive
	for rows.Next() {
		a, err := scanArchive(rows)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ListArchiveByExtKeyIn 按 `ext_json` 中指定键的**多值**等值匹配（批量版，避免逐行 N+1 查询）。
// values 为空时返回空结果，不执行 SQL。
func (d *DB) ListArchiveByExtKeyIn(
	ctx context.Context, ledgerType string, key ExtKey, values []string, rowSQL string, rowArgs []any,
) ([]LedgerArchive, error) {
	if ledgerType == "" || key == "" || len(values) == 0 {
		return nil, nil
	}
	uniq := make([]string, 0, len(values))
	seen := map[string]bool{}
	ph := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		uniq = append(uniq, v)
		ph = append(ph, "?")
	}
	if len(uniq) == 0 {
		return nil, nil
	}

	where := []string{
		"a.ledger_type = ?",
		"(a.biz_no IN (" + strings.Join(ph, ",") + ") OR (CASE WHEN json_valid(a.ext_json) " +
			"THEN json_extract(a.ext_json, ?) END) IN (" + strings.Join(ph, ",") + "))",
	}
	args := []any{ledgerType}
	for _, v := range uniq {
		args = append(args, v)
	}
	args = append(args, key.jsonPath())
	for _, v := range uniq {
		args = append(args, v)
	}
	if strings.TrimSpace(rowSQL) != "" {
		where = append(where, rowSQL)
		args = append(args, rowArgs...)
	}
	q := archiveSelectSQL + " WHERE " + strings.Join(where, " AND ") + " ORDER BY a.id ASC"

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 批量按 %s 关联查询台账失败: %w", key, err)
	}
	defer func() { _ = rows.Close() }()

	var out []LedgerArchive
	for rows.Next() {
		a, err := scanArchive(rows)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ExtString 从存档行的 `ext_json` 中读取指定键的字符串值（键不存在 / 脏 JSON / 非标量 → 空串）。
// 取值在 Go 侧做，避免为一两个字段再写一条 SQL；键名一律用约定的 ExtKey。
func (a *LedgerArchive) ExtString(key ExtKey) string {
	if a == nil || key == "" {
		return ""
	}
	raw := strings.TrimSpace(a.ExtJSON)
	if raw == "" || raw == "{}" || !json.Valid([]byte(raw)) {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	switch v := m[string(key)].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		// 数字型（金额等）按整数文本返回；调用方自行决定是否解析。
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}
