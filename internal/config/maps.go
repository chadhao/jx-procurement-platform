package config

import (
	"context"
	"strconv"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// Maps 聚合配置表装载结果（approval_code / field_id / ledger_type / threshold）。
// 口径全部来自 t_config_mapping，代码中不出现任何具体值（纪律 7、8）。
type Maps struct {
	Approval *ApprovalMap
	Field    *FieldMap
	// Ledger：doc_type → 台账类型语义键（L01..L12），**一对多**（B47 修复）。
	// ★ 例：`PR` 同时落 `L02`（采购需求与审批台账）与 `L03`（采购经办登记台账）。
	// 未配置 → 空切片（不写台账，避免虚构口径）。
	Ledger map[string][]string
	// Thresholds：阈值项（如 split_supplier_month=1000、spot_check_range=800-1000），单位元。
	Thresholds map[string]string
}

// LoadMaps 一次性装载全部配置映射。
func LoadMaps(ctx context.Context, db *store.DB) (*Maps, error) {
	approval, err := LoadApprovalMap(ctx, db)
	if err != nil {
		return nil, err
	}
	field, err := LoadFieldMap(ctx, db)
	if err != nil {
		return nil, err
	}
	ledger, err := loadLedgerMap(ctx, db)
	if err != nil {
		return nil, err
	}
	thresholds, err := loadSimpleMap(ctx, db, "threshold")
	if err != nil {
		return nil, err
	}
	return &Maps{Approval: approval, Field: field, Ledger: ledger, Thresholds: thresholds}, nil
}

// loadSimpleMap 装载 map_key → map_value 的简单映射（单值语义）。
func loadSimpleMap(ctx context.Context, db *store.DB, kind string) (map[string]string, error) {
	rows, err := db.ListConfigMappings(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		key := strings.TrimSpace(r.MapKey)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(r.MapValue)
	}
	return out, nil
}

// loadLedgerMap 装载 doc_type → []台账类型（**一对多**）。
//
// ★ 为什么与 loadSimpleMap 分开（B47 的根因就在这）：台账映射天然是一对多
// （一张单据可同时产生「需求与审批台账」与「经办登记台账」两条记录）。
// 用单值 map 装载会让**同一个 doc_type 的前一条被后一条覆盖**，且两条都在
// 配置文件里、校验也过 —— 典型的「配了却不生效且不报错」。
func loadLedgerMap(ctx context.Context, db *store.DB) (map[string][]string, error) {
	rows, err := db.ListConfigMappings(ctx, "ledger_type")
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(rows))
	for _, r := range rows {
		key := strings.TrimSpace(r.MapKey)
		val := strings.TrimSpace(r.MapValue)
		if key == "" || val == "" {
			continue
		}
		// 去重但保序（同一 (doc_type, ledger_type) 由 DB 唯一索引 + 导入校验双重保证，
		// 此处仅防御历史脏数据）。
		if !inList(val, out[key]) {
			out[key] = append(out[key], val)
		}
	}
	return out, nil
}

// LedgerTypesFor 返回 doc_type 对应的**全部**台账类型（一对多；B47）。
// 未配置返回 nil（调用方据此**不写台账**，避免虚构口径）。
//
// ★ 刻意**不提供**单值版本 `LedgerTypeFor`：单值 API 会把「一对多」的真实语义
// 压回一对一，是本次缺陷的温床（调用方写 `lt, ok := ...` 就默认只有一个）。
func (m *Maps) LedgerTypesFor(docType string) []string {
	if m == nil || m.Ledger == nil {
		return nil
	}
	return m.Ledger[strings.TrimSpace(docType)]
}

// LedgerMappingCount 返回已配置的 (doc_type → 台账) 映射**条数**（非 doc_type 个数），
// 供启动日志与自检使用 —— 一对多下按 doc_type 计数会低报。
func (m *Maps) LedgerMappingCount() int {
	if m == nil {
		return 0
	}
	n := 0
	for _, v := range m.Ledger {
		n += len(v)
	}
	return n
}

// ThresholdCents 解析单值阈值（单位元）为分。示例：split_supplier_month="1000" → 100000。
func (m *Maps) ThresholdCents(key string) (int64, bool) {
	if m == nil || m.Thresholds == nil {
		return 0, false
	}
	v, ok := m.Thresholds[key]
	if !ok || strings.TrimSpace(v) == "" {
		return 0, false
	}
	yuan, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, false
	}
	return int64(yuan*100 + 0.5), true
}

// ThresholdRangeCents 解析区间阈值（单位元，左闭右闭）为分。示例：spot_check_range="800-1000"。
func (m *Maps) ThresholdRangeCents(key string) (int64, int64, bool) {
	if m == nil || m.Thresholds == nil {
		return 0, 0, false
	}
	v, ok := m.Thresholds[key]
	if !ok {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimSpace(v), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	lo, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	hi, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return int64(lo*100 + 0.5), int64(hi*100 + 0.5), true
}
