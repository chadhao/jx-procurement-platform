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
	"regexp"
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
	ID               int            `json:"id"`
	Key              string         `json:"key"`
	Label            string         `json:"label"`
	SourceLedgers    []string       `json:"source_ledgers"`
	SourceStatus     string         `json:"source_status"` // connected / not_enabled / pending
	SourceNote       string         `json:"source_note"`
	OpsTableRequired bool           `json:"ops_table_required"`
	Indicators       []IndicatorDoc `json:"indicators"`
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
	// Render 指标形态（r8）：card / chart / alert / supervision —— 形态由规格定，
	// 实现按它归位（不再私设前缀区分形态 —— N-033② 的根源）。
	Render string `json:"render"`
}

// DashboardStatusConnected source_status 的唯一"接通"值。
const DashboardStatusConnected = "connected"

// dashboardRenderKinds render 值域（global_rules.r8）。
var dashboardRenderKinds = map[string]bool{"card": true, "chart": true, "alert": true, "supervision": true}

// opsLedgerListRe 从 r2 **正文**提取「须读运营表」的台账名单（[D5]）——
// ★ 名单只此一份：写在 r2 文本里，代码不另起清单（否则又是两份真相）。
var opsLedgerListRe = regexp.MustCompile(`L[0-9]{2}`)

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
//	D4 每张指标 key/label/formula 非空，且看板内 key 唯一（重复 key＝渲染撞车）；
//	   render 必填且 ∈{card,chart,alert,supervision}（r8 形态由规格定）
//	D5 source_ledgers ∩（r2 正文提取的运营表名单）≠ ∅ ⇒ 必须 ops_table_required==true
//	D7 source_ledgers == 全部指标 fields 引用台账的并集（r9 派生量；多列与漏列都要报）
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
			if !dashboardRenderKinds[ind.Render] {
				problems = append(problems, fmt.Sprintf(
					"[D4] 看板 %d 指标 %q render=%q 不在值域 card/chart/alert/supervision（r8：形态由规格定）",
					b.ID, ind.Key, ind.Render))
			}
			if keys[ind.Key] {
				problems = append(problems, fmt.Sprintf("[D4] 看板 %d 指标 key %q 重复", b.ID, ind.Key))
			}
			keys[ind.Key] = true
		}
		// D5：source_ledgers 命中 r2 运营表名单 ⇒ 必须显式声明 ops_table_required
		opsList := map[string]bool{}
		for _, led := range opsLedgerListRe.FindAllString(doc.GlobalRules.R2SourceMustBeOpsTable, -1) {
			opsList[led] = true
		}
		if len(opsList) == 0 {
			problems = append(problems, "[D5] r2 正文未能提取出任何台账名单 —— 判据失去依据（r2 文本被改写？）")
		}
		hit := false
		for _, led := range b.SourceLedgers {
			if opsList[led] {
				hit = true
			}
		}
		if hit && !b.OpsTableRequired {
			problems = append(problems, fmt.Sprintf(
				"[D5] 看板 %d 的 source_ledgers 与 r2 运营表名单相交，但未声明 ops_table_required=true（r2：数据源必须指向运营表）",
				b.ID))
		}

		// D7：source_ledgers 必须 == indicators[*].fields 的台账并集（r9 派生量，双向报错）
		derived := map[string]bool{}
		for _, ind := range b.Indicators {
			for _, f := range ind.Fields {
				if len(f) >= 4 && f[0] == 'L' && f[1] >= '0' && f[1] <= '9' &&
					f[2] >= '0' && f[2] <= '9' && f[3] == '.' {
					derived[f[:3]] = true
				}
			}
		}
		declared := map[string]bool{}
		for _, led := range b.SourceLedgers {
			declared[led] = true
		}
		for led := range declared {
			if !derived[led] {
				problems = append(problems, fmt.Sprintf(
					"[D7] 看板 %d source_ledgers 多列 %s（没有任何指标 fields 引用 —— 手写冗余必漂移，r9）",
					b.ID, led))
			}
		}
		for led := range derived {
			if !declared[led] {
				problems = append(problems, fmt.Sprintf(
					"[D7] 看板 %d source_ledgers 漏列 %s（指标 fields 引用了但未声明 —— 派生量必须与并集相等，r9）",
					b.ID, led))
			}
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
