package specload

// authority.json 消费端地基（N-028）——「授权配置」（四分法第四类）的类型化读取。
//
// ★ 与 constants.json 的分界（authority_split.decision_test）：改它会不会改变
//   「谁能审」？会 ⇒ 授权配置（本文件，有显式约束兜底）；不会 ⇒ 运营性常量。
// 消费点：
//   - /api/admin/role-agents CRUD 白名单（agent_eligible_roles.eligible）
//   - chain.NodeAllowsAgent（备付金按节点排除 —— 从 tier1 节点 id 动态读）
//   - feature_enabled 标注（enable_guard：解除条件已达成 ⇒ true，N-072 撤销告示；
//     本文件只解析 enable_guard 的规则文本，不改解析行为）

import (
	"encoding/json"
	"fmt"
)

// AuthorityDoc spec/authority.json 顶层。
type AuthorityDoc struct {
	Version     string            `json:"version"`
	Eligible    AuthorityEligible `json:"agent_eligible_roles"`
	EnableGuard json.RawMessage   `json:"enable_guard"`
}

// AuthorityEligible 可设代理人的角色白名单（+ 4 个显式排除的角色）。
type AuthorityEligible struct {
	Rule     string `json:"rule"`
	Excluded []struct {
		Role  string `json:"role"`
		Label string `json:"label"`
		Why   string `json:"why"`
	} `json:"excluded"`
	Eligible []string `json:"eligible"`
}

// IsEligibleRole 角色是否可配置代理人（白名单；applicant/sys_admin/group_finance/
// group_approval 因不在名单内天然被拒）。
func (a *AuthorityDoc) IsEligibleRole(roleKey string) bool {
	if a == nil {
		return false
	}
	for _, r := range a.Eligible.Eligible {
		if r == roleKey {
			return true
		}
	}
	return false
}

func decodeAuthority(files map[string][]byte) (*AuthorityDoc, error) {
	raw, ok := files["spec/authority.json"]
	if !ok {
		return nil, fmt.Errorf("specload: 缺少 spec/authority.json（授权配置登记册）")
	}
	doc := &AuthorityDoc{}
	if err := json.Unmarshal(raw, doc); err != nil {
		return nil, fmt.Errorf("specload: spec/authority.json 解析失败: %w", err)
	}
	return doc, nil
}

// validateAuthority [A] 系列自检：
//
//	A1 eligible 白名单非空（空名单＝整类配置不可用却无人知晓）
//	A2 excluded 的 4 个角色不得出现在 eligible（出现即口径自相矛盾）
func validateAuthority(doc *AuthorityDoc) []string {
	problems := []string{}
	if doc == nil {
		return []string{"[A1] authority.json 未加载"}
	}
	if len(doc.Eligible.Eligible) == 0 {
		problems = append(problems, "[A1] agent_eligible_roles.eligible 为空")
	}
	inEligible := map[string]bool{}
	for _, r := range doc.Eligible.Eligible {
		inEligible[r] = true
	}
	for _, ex := range doc.Eligible.Excluded {
		if inEligible[ex.Role] {
			problems = append(problems, fmt.Sprintf(
				"[A2] 角色 %q 同时出现在 excluded 与 eligible（口径自相矛盾）", ex.Role))
		}
	}
	return problems
}
