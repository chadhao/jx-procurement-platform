package chain

import "strings"

// 代理人节点适用面（N-031 · 制度第十二条「备付金审批不得代理」的系统执行体）。
//
// ★★ **业务名单在 spec、不在代码**（与 checks.json「判据＝数据」同一条纪律）：
//   禁代理节点由 `chain.json#routes.*.nodes[*].agent_allowed == false` 标记
//   （conventions.agent_allowed：缺省 true）。本文件**不出现任何节点 id 字面量** ——
//   改链 / 新增禁代理节点时，消费端自动跟随，不会静默漏拦。
// ★ **按节点排除，不按角色排除**：`ops_supervisor` 在采二/采三档的 `ops_review` 初审
//   仍可由代理人操作（按角色一刀切禁掉它＝误伤初审，与漏拦同样致命）。
// ★ 当前消费端＝M9（转交/回退的节点级校验，未落地）＋ /admin role-agents 列表回传；
//   本函数先落地并被**鉴别性探针**锚定（N-031：注入第 3 个标 false 的节点必须被拒）。

import (
	"fmt"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// AgentDeniedNodeIDs 从 spec 全部流程线收集 `agent_allowed == false` 的节点 id。
func AgentDeniedNodeIDs(b *specload.Bundle) map[string]bool {
	return DeniedNodeIDsFromRoutes(b.Chain.Routes)
}

// DeniedNodeIDsFromRoutes 纯函数版（供鉴别性探针注入合成节点表）：
// 遍历 routes.*.nodes，收 `AgentAllowed != nil && *AgentAllowed == false` 的 id。
func DeniedNodeIDsFromRoutes(routes map[string]specload.RouteDoc) map[string]bool {
	out := map[string]bool{}
	for _, route := range routes {
		for _, n := range route.Nodes {
			if n.AgentAllowed != nil && !*n.AgentAllowed {
				out[n.ID] = true
			}
		}
	}
	return out
}

// NodeAllowsAgent 该链节点是否接受代理人操作（M9 将在转交/回退入口调用）。
// 返回 (allowed, reason)；reason 非空时调用方应可见拒绝。
func NodeAllowsAgent(b *specload.Bundle, sourceNodeID string) (bool, string) {
	if AgentDeniedNodeIDs(b)[sourceNodeID] {
		return false, fmt.Sprintf("节点 %q 标记为不可代理（agent_allowed=false —— 制度第十二条：备付金审批不得代理）", sourceNodeID)
	}
	return true, ""
}

// FindRoleOfNode 节点 id → 审批角色（N-060 F4 · 代理人正向消费的第一重判定）：
//   - 全 routes 精确匹配 nodes[*].id 且 actor ∈ approverRoles ⇒ 返回该 actor；
//   - 展开型节点 id（`<node>_<role>`，如 tier_chain_supervisor）⇒ 后缀 role ∈ approverRoles；
//   - `contract_pgm` 特例后缀 pgm ⇒ project_general_manager（expandContract 命名族）；
//   - 找不到 ⇒ ok=false（保守 fail-closed：不可代理）。
//
// ★ 纯 spec 扫描、不依赖 store —— chain 不连库（RoleSource 由装配层适配的既有架构）。
func FindRoleOfNode(b *specload.Bundle, nodeID string) (string, bool) {
	if b == nil || nodeID == "" {
		return "", false
	}
	for _, rt := range b.Chain.Routes {
		for _, n := range rt.Nodes {
			if n.ID == nodeID && approverRoles[n.Actor] {
				return n.Actor, true
			}
		}
	}
	if i := strings.LastIndex(nodeID, "_"); i > 0 {
		if role := nodeID[i+1:]; approverRoles[role] {
			return role, true
		}
	}
	if strings.HasSuffix(nodeID, "_pgm") {
		return "project_general_manager", true
	}
	return "", false
}
