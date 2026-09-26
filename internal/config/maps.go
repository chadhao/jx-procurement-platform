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
	// Ledger：doc_type → 台账类型语义键（L01..L12）。★ 该映射口径属 Q14/Q6 待定，未配置则不写台账。
	Ledger map[string]string
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
	ledger, err := loadSimpleMap(ctx, db, "ledger_type")
	if err != nil {
		return nil, err
	}
	thresholds, err := loadSimpleMap(ctx, db, "threshold")
	if err != nil {
		return nil, err
	}
	return &Maps{Approval: approval, Field: field, Ledger: ledger, Thresholds: thresholds}, nil
}

// loadSimpleMap 装载 map_key → map_value 的简单映射。
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

// LedgerTypeFor 返回 doc_type 对应的台账类型；未配置返回 false（则不写台账，避免虚构口径）。
func (m *Maps) LedgerTypeFor(docType string) (string, bool) {
	if m == nil || m.Ledger == nil {
		return "", false
	}
	v, ok := m.Ledger[strings.TrimSpace(docType)]
	return v, ok && v != ""
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
