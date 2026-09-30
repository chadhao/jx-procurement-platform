package chain

// 代理人节点适用面（N-028 checks#no_agent_for_petty_cash）——
// ★ **按节点排除，不按角色排除**：`ops_supervisor` 在采二/采三档的 `ops_review` 初审
//   **仍可**由代理人操作（按角色一刀切禁掉它＝误伤初审，与漏拦同样致命）。
// ★ 备付金节点（制度「备付金审批不得代理」）：approve_petty_cash / disburse 不接受代理人。
// ★ 当前消费端＝M9（转交/回退的节点级校验，未落地）；本函数先落地并被
//   「应拦/应放行」用例锚定（spec 驱动：节点 id 从 chain.json tier1 动态读，不硬编码）。

import (
	"fmt"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// PettyCashAgentDeniedNodeIDs 从 spec 读出「不接受代理人」的节点 id 集合
// （chain.json#routes.purchase_tier1.nodes 中制度点名的两个：approve_petty_cash / disburse）。
func PettyCashAgentDeniedNodeIDs(b *specload.Bundle) map[string]bool {
	out := map[string]bool{}
	route, ok := b.Chain.Routes["purchase_tier1"]
	if !ok {
		return out
	}
	for _, n := range route.Nodes {
		if n.ID == "approve_petty_cash" || n.ID == "disburse" {
			out[n.ID] = true
		}
	}
	return out
}

// NodeAllowsAgent 该链节点是否接受代理人操作（M9 将在转交/回退入口调用）。
// 返回 (allowed, reason)；reason 非空时调用方应可见拒绝。
func NodeAllowsAgent(b *specload.Bundle, sourceNodeID string) (bool, string) {
	denied := PettyCashAgentDeniedNodeIDs(b)
	if denied[sourceNodeID] {
		return false, fmt.Sprintf("节点 %q 属备付金环节（制度第十二条：备付金审批不得代理）", sourceNodeID)
	}
	return true, ""
}
