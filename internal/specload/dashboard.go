package specload

// dashboard.json 类型化解析 + [D] 系列自检（看板规格 V1.0 · WorkBuddy 交付 2026-10-01）。
//
// ★ global_rules 三条纪律是本文件的骨架（"不要让「没有数据」与「没有违规」长得一样"）：
//   - r1 数据源未接通 ⇒ 显示「数据未接入」而非 0（source_status ∈ connected/not_enabled/pending）
//   - r2 数据源必须指向运营表（L03/L04/L05/L06/L07/L11 等须读可写表）
//   - r3 「应恒为 0」类指标必须带数据源非空前置断言（R-02 实证）

import (
	"encoding/json"
	"fmt"
)

// DashboardDoc spec/dashboard.json 顶层。
type DashboardDoc struct {
	Version     string           `json:"version"`
	GlobalRules DashboardRules   `json:"global_rules"`
	Dashboards  []DashboardBoard `json:"dashboards"`
	KnownGaps   []map[string]any `json:"known_gaps"`
}

// DashboardRules global_rules —— 只必填 r1/r2/r3（r4/r5 为历史参照，保留原文不解析）。
type DashboardRules struct {
	R1NotConnectedShowsGap   string `json:"r1_not_connected_shows_gap"`
	R2SourceMustBeOpsTable   string `json:"r2_source_must_be_ops_table"`
	R3ZeroNeedsNonemptyGuard string `json:"r3_zero_expected_needs_nonempty_guard"`
}

// DashboardBoard 单张看板（id 13–16）。
type DashboardBoard struct {
	ID            int            `json:"id"`
	Key           string         `json:"key"`
	Label         string         `json:"label"`
	SourceLedgers []string       `json:"source_ledgers"`
	SourceStatus  string         `json:"source_status"` // connected / not_enabled / pending
	SourceNote    string         `json:"source_note"`
	Indicators    []IndicatorDoc `json:"indicators"`
}

// IndicatorDoc 看板指标（key/label/formula 三要素缺一不可 —— 机读规格必须可机检）。
// AvailabilityGuard 指标级自证声明（global_rules.r6）：connected 看板的每个指标必填。
type IndicatorDoc struct {
	Key               string   `json:"key"`
	Label             string   `json:"label"`
	Formula           string   `json:"formula"`
	Fields            []string `json:"fields"`
	Note              string   `json:"note"`
	AvailabilityGuard string   `json:"availability_guard"`
}

// DashboardStatusConnected source_status 的唯一"接通"值。
const DashboardStatusConnected = "connected"

// dashboardSourceStatuses source_status 值域。
var dashboardSourceStatuses = map[string]bool{
	"connected": true, "not_enabled": true, "pending": true,
}

// Board 按 id 取看板（未找到返回 nil）。
func (d *DashboardDoc) Board(id int) *DashboardBoard {
	if d == nil {
		return nil
	}
	for i := range d.Dashboards {
		if d.Dashboards[i].ID == id {
			return &d.Dashboards[i]
		}
	}
	return nil
}

// decodeDashboard 解析 spec/dashboard.json（V1.0 起为必载文件：机读规格必须可机检）。
func decodeDashboard(files map[string][]byte) (*DashboardDoc, error) {
	raw, ok := files["spec/dashboard.json"]
	if !ok {
		return nil, fmt.Errorf("specload: 缺少 spec/dashboard.json（看板规格）")
	}
	doc := &DashboardDoc{}
	if err := json.Unmarshal(raw, doc); err != nil {
		return nil, fmt.Errorf("specload: spec/dashboard.json 解析失败: %w", err)
	}
	return doc, nil
}

// validateDashboard [D] 系列自检：
//
//	D1 恰 4 张看板、id 集合恰为 {13,14,15,16}（缺一张＝对应看板静默失联）
//	D2 global_rules r1/r2/r3 必填非空（三条纪律是骨架，缺一条=纪律失传）
//	D3 每张看板 key/label/source_ledgers 非空，source_status ∈ 值域
//	D4 每张指标 key/label/formula 非空，且看板内 key 唯一（重复 key＝渲染撞车）
//	D6 source_status==connected 的看板 ⇒ 每个指标必须声明 availability_guard
//	   （r6 的「守卫齐全」判据机检化 —— 否则 connected 只是口头约定，没数据的指标会显示 0）
func validateDashboard(doc *DashboardDoc) []string {
	if doc == nil {
		return []string{"[D1] dashboard.json 未加载"}
	}
	problems := []string{}

	// D1
	wantIDs := map[int]bool{13: true, 14: true, 15: true, 16: true}
	seen := map[int]bool{}
	for _, b := range doc.Dashboards {
		if !wantIDs[b.ID] {
			problems = append(problems, fmt.Sprintf("[D1] 未登记的看板 id=%d（只允许 13–16）", b.ID))
		}
		if seen[b.ID] {
			problems = append(problems, fmt.Sprintf("[D1] 看板 id=%d 重复", b.ID))
		}
		seen[b.ID] = true
	}
	for id := range wantIDs {
		if !seen[id] {
			problems = append(problems, fmt.Sprintf("[D1] 缺看板 id=%d（4 张必须齐）", id))
		}
	}

	// D2
	if doc.GlobalRules.R1NotConnectedShowsGap == "" {
		problems = append(problems, "[D2] global_rules.r1_not_connected_shows_gap 为空")
	}
	if doc.GlobalRules.R2SourceMustBeOpsTable == "" {
		problems = append(problems, "[D2] global_rules.r2_source_must_be_ops_table 为空")
	}
	if doc.GlobalRules.R3ZeroNeedsNonemptyGuard == "" {
		problems = append(problems, "[D2] global_rules.r3_zero_expected_needs_nonempty_guard 为空")
	}

	// D3/D4
	for _, b := range doc.Dashboards {
		if b.Key == "" {
			problems = append(problems, fmt.Sprintf("[D3] 看板 %d 缺 key", b.ID))
		}
		if b.Label == "" {
			problems = append(problems, fmt.Sprintf("[D3] 看板 %d 缺 label", b.ID))
		}
		if len(b.SourceLedgers) == 0 {
			problems = append(problems, fmt.Sprintf("[D3] 看板 %d 缺 source_ledgers", b.ID))
		}
		if !dashboardSourceStatuses[b.SourceStatus] {
			problems = append(problems, fmt.Sprintf("[D3] 看板 %d source_status=%q 不在值域 connected/not_enabled/pending",
				b.ID, b.SourceStatus))
		}
		keys := map[string]bool{}
		for i, ind := range b.Indicators {
			if ind.Key == "" {
				problems = append(problems, fmt.Sprintf("[D4] 看板 %d 第 %d 个指标缺 key", b.ID, i))
			}
			if ind.Label == "" {
				problems = append(problems, fmt.Sprintf("[D4] 看板 %d 指标 %q 缺 label", b.ID, ind.Key))
			}
			if ind.Formula == "" {
				problems = append(problems, fmt.Sprintf("[D4] 看板 %d 指标 %q 缺 formula（无公式=口径不可追溯）",
					b.ID, ind.Key))
			}
			if keys[ind.Key] {
				problems = append(problems, fmt.Sprintf("[D4] 看板 %d 指标 key %q 重复", b.ID, ind.Key))
			}
			keys[ind.Key] = true
		}
		// D6
		if b.SourceStatus == DashboardStatusConnected {
			for _, ind := range b.Indicators {
				if ind.AvailabilityGuard == "" {
					problems = append(problems, fmt.Sprintf(
						"[D6] 看板 %d 已 connected 但指标 %q 未声明 availability_guard（r6 判据＝守卫齐全；缺守卫的指标没数据时会显示 0）",
						b.ID, ind.Key))
				}
			}
		}
	}
	return problems
}
