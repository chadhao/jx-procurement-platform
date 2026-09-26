package config

import (
	"context"
	"sort"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ApprovalMap 承载 approval_code → 单据类型（doc_type）映射。
// ★ 具体值不在代码中出现，全部来自 t_config_mapping(map_kind='approval_code')（PRD Q1，纪律 8）。
type ApprovalMap struct {
	byCode map[string]string
}

// LoadApprovalMap 从配置表装载 approval_code 映射。
func LoadApprovalMap(ctx context.Context, db *store.DB) (*ApprovalMap, error) {
	rows, err := db.ListConfigMappings(ctx, "approval_code")
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		key := strings.TrimSpace(r.MapKey)
		if key == "" {
			continue
		}
		m[key] = strings.TrimSpace(r.MapValue)
	}
	return &ApprovalMap{byCode: m}, nil
}

// DocType 返回 approval_code 对应的单据类型。
func (a *ApprovalMap) DocType(code string) (string, bool) {
	if a == nil {
		return "", false
	}
	v, ok := a.byCode[strings.TrimSpace(code)]
	return v, ok && v != ""
}

// Codes 返回全部已配置的 approval_code（升序），供启动订阅与对账遍历。
func (a *ApprovalMap) Codes() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.byCode))
	for k := range a.byCode {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Len 返回映射条目数。
func (a *ApprovalMap) Len() int {
	if a == nil {
		return 0
	}
	return len(a.byCode)
}
