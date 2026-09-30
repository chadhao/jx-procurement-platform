package specload

// params.json 消费端地基（T1 / N-025）——「可配置参数」的类型化读取。
//
// ★ 铁律（README 定案 #24）：**每个参数必须真被读**，否则是「假配置」。
//   本包只提供读取通道；消费点：
//   - `reporting.monthly_cutoff_day` / `reporting.overdue_handling`
//       → httpapi 报销登记（提交批次归集 + 超期处置，pending 标记）
//   - `petty_cash.reimburse_deadline`
//       → httpapi 备付金月核销（type=none ⇒ 明确「不设时限、不阻断」分支）
//   - `contract.amount_over_pr_tolerance_percent` / `contract.over_tolerance_action`
//       → CT 提交校验 amount_vs_pr（hard，阈值读本参数不写死）
//   加载即校验：缺 consumer 声明的参数 ⇒ 拒启（防「声明了没人读」再次发生）。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParamsDoc spec/params.json 顶层。
type ParamsDoc struct {
	Version   string                `json:"version"`
	Params    map[string]ParamEntry `json:"params"`
	OpenItems []json.RawMessage     `json:"open_items"`
}

// ParamEntry 单个参数（value 可为任意 JSON：数字 / 字符串 / null）。
type ParamEntry struct {
	Value          json.RawMessage `json:"value"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	Desc           string          `json:"desc"`
	Recommendation string          `json:"recommendation"`
	Consumer       []string        `json:"consumer"`
}

// IsPending 待定参数 —— **以 status 为准**：仅 `status == "待定"` 才是待定。
// ★ 注意区分：`value: null` 且 `type: "none"`（如 petty_cash.reimburse_deadline）
//
//	是「**已定的不设时限**」，不是待定 —— 用 null 判待定会把用户定案误伤成未决。
//	待定参数（overdue_handling）消费方须按 recommendation 建议值运行并标 pending，
//	不得把建议值固化成「已定」。
func (e ParamEntry) IsPending() bool {
	return e.Status == "待定"
}

// ParamEntryByKey 取参数；不存在返回 false。
func (b *Bundle) ParamEntryByKey(key string) (ParamEntry, bool) {
	if b == nil || b.Params == nil || b.Params.Params == nil {
		return ParamEntry{}, false
	}
	e, ok := b.Params.Params[key]
	return e, ok
}

// ParamInt 读整数参数（value 为 JSON number）。
func (b *Bundle) ParamInt(key string) (int, bool) {
	e, ok := b.ParamEntryByKey(key)
	if !ok || e.IsPending() {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(e.Value, &n); err != nil {
		return 0, false
	}
	return n, true
}

// ParamString 读字符串参数。
func (b *Bundle) ParamString(key string) (string, bool) {
	e, ok := b.ParamEntryByKey(key)
	if !ok || e.IsPending() {
		return "", false
	}
	var s string
	if err := json.Unmarshal(e.Value, &s); err != nil {
		return "", false
	}
	return s, true
}

// ParamPending 读「待定」参数：返回 (是否待定, recommendation 文本)。
// 消费方规则：待定 ⇒ 按建议值行为运行 + 对外标 pending，**不硬编码成已定值**。
func (b *Bundle) ParamPending(key string) (bool, string) {
	e, ok := b.ParamEntryByKey(key)
	if !ok {
		return false, ""
	}
	return e.IsPending(), e.Recommendation
}

// ---------------------------------------------------------------------------
// 加载与自检
// ---------------------------------------------------------------------------

func decodeParams(files map[string][]byte) (*ParamsDoc, error) {
	raw, ok := files["spec/params.json"]
	if !ok {
		return nil, fmt.Errorf("specload: 缺少 spec/params.json（可配置参数登记册）")
	}
	doc := &ParamsDoc{}
	if err := json.Unmarshal(raw, doc); err != nil {
		return nil, fmt.Errorf("specload: spec/params.json 解析失败: %w", err)
	}
	return doc, nil
}

// validateParams 参数登记册自检（[P] 系列，Go 装载器自有 —— README #24 的机器化）：
//
//	P1 每个参数必须声明非空 consumer（「声明了没人读」在**声明侧**先拦一道；
//	   运行侧由消费函数的存在保证，两侧合力）
//	P2 每个参数必须声明 status（已定 / 待定）
//	P3 待定参数必须带 recommendation（允许「按建议值运行」）
func validateParams(doc *ParamsDoc) []string {
	problems := []string{}
	if doc == nil {
		return []string{"[P1] params.json 未加载"}
	}
	for key, e := range doc.Params {
		if len(e.Consumer) == 0 {
			problems = append(problems, fmt.Sprintf("[P1] 参数 %q 未声明 consumer（README 定案 #24：假配置）", key))
		}
		if strings.TrimSpace(e.Status) == "" {
			problems = append(problems, fmt.Sprintf("[P2] 参数 %q 缺 status", key))
		}
		if e.IsPending() && strings.TrimSpace(e.Recommendation) == "" {
			problems = append(problems, fmt.Sprintf("[P3] 待定参数 %q 缺 recommendation（须可按建议值运行）", key))
		}
	}
	return problems
}
