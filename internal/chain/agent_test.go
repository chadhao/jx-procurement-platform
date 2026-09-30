package chain

// N-031 验收：禁代理名单**数据驱动**（读 spec 的 agent_allowed 标记）+ 鉴别性探针。
//
// ★ 探针设计（WorkBuddy 议题原文）：注入**第 3 个**标 `agent_allowed:false` 的合成节点
//   ⇒ 必须被拒。**字面量实现会放行**（名单里没有它）⇒ 该用例在旧实现下**必须红** ——
//   这就是「修复前后行为相反」的判据（N-026 同族：期望值不得与被测对象同源）。

import (
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func boolPtr(v bool) *bool { return &v }

func TestNodeAllowsAgent(t *testing.T) {
	b := loadBundle(t)

	// ① spec 真源：tier1 两个备付金节点已标 agent_allowed=false ⇒ 拒
	for _, id := range []string{"approve_petty_cash", "disburse"} {
		allowed, reason := NodeAllowsAgent(b, id)
		if allowed {
			t.Errorf("节点 %s 应被拒（spec 标记 agent_allowed=false），却放行", id)
		}
		if reason == "" {
			t.Errorf("节点 %s 拒绝时应给出可见 reason", id)
		}
	}

	// ② 应放行：ops_supervisor 在采二档的初审节点（按角色一刀切才会误伤这里）
	for _, id := range []string{"ops_review", "supervisor_approval", "submit_group"} {
		if allowed, reason := NodeAllowsAgent(b, id); !allowed {
			t.Errorf("节点 %s 应允许代理人，却拒绝：%s", id, reason)
		}
	}

	// ③ 真源锚定：标记确实来自 spec（agent_allowed 字段，而非代码常量）
	tier1 := b.Chain.Routes["purchase_tier1"]
	marked := 0
	for _, n := range tier1.Nodes {
		if n.AgentAllowed != nil && !*n.AgentAllowed {
			marked++
		}
	}
	if marked != 2 {
		t.Errorf("spec tier1 中 agent_allowed=false 的节点应为 2 个，实为 %d（spec 侧标记丢失？）", marked)
	}
}

// TestAgentDeniedDataDrivenProbe ★★ 鉴别性探针（N-031 核心验收）：
// 合成节点表里加入第 3 个标 false 的未来节点 ⇒ 必须被收集进拒绝名单。
// 字面量实现（枚举 approve_petty_cash/disburse）会**放行 future_node** ⇒ 本用例必须红。
func TestAgentDeniedDataDrivenProbe(t *testing.T) {
	routes := map[string]specload.RouteDoc{
		"synthetic": {Nodes: []specload.NodeDoc{
			{ID: "approve_petty_cash", AgentAllowed: boolPtr(false)},
			{ID: "disburse", AgentAllowed: boolPtr(false)},
			{ID: "future_added_node", AgentAllowed: boolPtr(false)}, // ★ 注入的第 3 个
			{ID: "ops_review"},     // 缺省（nil）⇒ 允许
			{ID: "new_route_node"}, // 缺省 ⇒ 允许
		}},
	}
	denied := DeniedNodeIDsFromRoutes(routes)
	if len(denied) != 3 {
		t.Fatalf("应收集 3 个禁代理节点（含注入项），实为 %d：%v", len(denied), denied)
	}
	if !denied["future_added_node"] {
		t.Error("★ 注入的 agent_allowed=false 节点未被拒绝 —— 实现疑似仍用字面量名单（N-031）")
	}
	if denied["ops_review"] || denied["new_route_node"] {
		t.Errorf("缺省（未标记）的节点不应进拒绝名单：%v", denied)
	}

	// 新增流程线上的标记同样生效（改链不静默漏拦）
	routes["brand_new_route"] = specload.RouteDoc{Nodes: []specload.NodeDoc{
		{ID: "some_new_petty_node", AgentAllowed: boolPtr(false)},
	}}
	if d2 := DeniedNodeIDsFromRoutes(routes); !d2["some_new_petty_node"] {
		t.Error("新增流程线上的 agent_allowed=false 未被收集 —— 改链会静默漏拦")
	}
}
