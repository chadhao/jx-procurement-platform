package specload

// constants.json 消费端地基（T2 / R-24 用户 A4 定案）——运营性常量表的类型化读取。
//
// ★ 三分法（three_way_split.decision_test）：改掉这个值**会不会改变审批链或校验结论**？
//   会 ⇒ 制度性枚举（enums.json，后台不可改）；不会 ⇒ 运营性常量（本表，后台可改）。
// ★ policy 要点：只停用不删 · 单据存值快照 · 增删改写审计 · role_display_name 禁增删角色。
// 消费点：
//   - 启动播种（cmd/jxapproval → seedConstants，INSERT OR IGNORE，**只是可用起点**）
//   - GET /api/approval/meta 下发（constant_ref 字段渲染下拉）
//   - 提交校验 + 值快照（httpapi submit：<字段>_snapshot 冻结历史显示）
//   - /api/admin/constants CRUD（守护栏：retire_only + 角色表只改名）

import (
	"encoding/json"
	"fmt"
)

// ConstantsDoc spec/constants.json 顶层。
type ConstantsDoc struct {
	Version   string            `json:"version"`
	Tables    []ConstantTable   `json:"tables"`
	OpenItems []json.RawMessage `json:"open_items"`
}

// ConstantTable 一张常量表（unit / role_display_name / contract_template）。
type ConstantTable struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	UsedBy       []string `json:"used_by"`
	Mutable      bool     `json:"mutable"`
	DeletePolicy string   `json:"delete_policy"` // retire_only
	Seed         []string `json:"seed"`          // ★ 可用起点，不是权威清单
	CriticalNote string   `json:"critical_note"`
}

// TableByKey 取表定义。
func (c *ConstantsDoc) TableByKey(key string) (ConstantTable, bool) {
	if c == nil {
		return ConstantTable{}, false
	}
	for _, t := range c.Tables {
		if t.Key == key {
			return t, true
		}
	}
	return ConstantTable{}, false
}

func decodeConstants(files map[string][]byte) (*ConstantsDoc, error) {
	raw, ok := files["spec/constants.json"]
	if !ok {
		return nil, fmt.Errorf("specload: 缺少 spec/constants.json（运营性常量登记册）")
	}
	doc := &ConstantsDoc{}
	if err := json.Unmarshal(raw, doc); err != nil {
		return nil, fmt.Errorf("specload: spec/constants.json 解析失败: %w", err)
	}
	return doc, nil
}

// validateConstants 常量表自检（[C] 系列，装载器自有）：
//
//	C1 每张表必须有 key / label / seed 非空（空表没有存在意义 —— 「配了没人用」的反面）
//	C2 key 不得重复；delete_policy 必须是 retire_only（R-24：只停用不删）
func validateConstants(doc *ConstantsDoc) []string {
	problems := []string{}
	if doc == nil {
		return []string{"[C1] constants.json 未加载"}
	}
	seen := map[string]bool{}
	for _, t := range doc.Tables {
		if t.Key == "" || t.Label == "" {
			problems = append(problems, fmt.Sprintf("[C1] 常量表存在空 key/label（key=%q label=%q）", t.Key, t.Label))
		}
		if len(t.Seed) == 0 {
			problems = append(problems, fmt.Sprintf("[C1] 常量表 %q 的 seed 为空（应至少给可用起点）", t.Key))
		}
		if seen[t.Key] {
			problems = append(problems, fmt.Sprintf("[C2] 常量表 key 重复：%q", t.Key))
		}
		seen[t.Key] = true
		if t.DeletePolicy != "retire_only" {
			problems = append(problems, fmt.Sprintf("[C2] 常量表 %q 的 delete_policy=%q，必须为 retire_only（R-24 只停用不删）",
				t.Key, t.DeletePolicy))
		}
	}
	return problems
}
