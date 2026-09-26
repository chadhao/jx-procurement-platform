package config

import (
	"context"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// FieldMap 承载「字段 id → 业务字段名」映射。
// 同一控件 id 在不同模板可能含义不同，故以 (doc_type, field_id) 为键（架构 §6.2）。
// ★ 具体控件 id 一律待确认（PRD Q1），代码中不得出现具体值。
type FieldMap struct {
	byDocField map[string]string
}

// LoadFieldMap 从配置表装载 field_id 映射（map_kind='field_id'）。
func LoadFieldMap(ctx context.Context, db *store.DB) (*FieldMap, error) {
	rows, err := db.ListConfigMappings(ctx, "field_id")
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		fieldID := strings.TrimSpace(r.MapKey)
		if fieldID == "" {
			continue
		}
		m[fieldMapKey(r.DocType, fieldID)] = strings.TrimSpace(r.MapValue)
	}
	return &FieldMap{byDocField: m}, nil
}

// BizField 返回 (doc_type, field_id) 对应的业务字段名；未映射返回 false。
// 未映射字段落库但不进业务列，不报错（TC-23）。
func (f *FieldMap) BizField(docType, fieldID string) (string, bool) {
	if f == nil {
		return "", false
	}
	// 先按 (doc_type, field_id) 精确匹配，再退回通配 doc_type="" 的全局映射。
	if v, ok := f.byDocField[fieldMapKey(docType, fieldID)]; ok && v != "" {
		return v, true
	}
	if v, ok := f.byDocField[fieldMapKey("", fieldID)]; ok && v != "" {
		return v, true
	}
	return "", false
}

func fieldMapKey(docType, fieldID string) string {
	return strings.TrimSpace(docType) + "\x00" + strings.TrimSpace(fieldID)
}
