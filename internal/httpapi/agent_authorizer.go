package httpapi

// N-060 F4 · 代理人正向消费门（FR-M9-04/06「本人**或其代理人**」）。
//
// 三重判定（缺一即 false ⇒ 保守回落「仅本人」，fail-closed）：
//  1. task 节点 → 审批角色（chain.FindRoleOfNode：精确 nodes[*].id / 展开型后缀；
//     找不到 ⇒ false —— 动态/动作节点不开放代理）；
//  2. 该角色的 **active** 代理 = actor（store.FindActiveRoleAgent —— N-028 表；
//     离职/停用/state≠active 由该查询天然排除）；
//  3. chain.NodeAllowsAgent(bundle, nodeID)（authority 备付金等按节点排除 —— N-031）。
//
// ★ 装配：bootstrap 注入 flow.Service.SetAgentAuthorizer；AddSign 不经过此门
// （01a §4.1 表二：代理人不可加签）。
// ★ 审计语义：OpLog.ActorOpenID 恒为**真实操作人**（代理人本人），不伪装 assignee。

import (
	"context"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// NewTaskAgentAuthorizer 构造代理人正向消费门（nil bundle/db ⇒ 恒 false）。
func NewTaskAgentAuthorizer(bundle *specload.Bundle, db *store.DB) func(context.Context, *store.FlowTask, string) bool {
	return func(ctx context.Context, task *store.FlowTask, actorOpenID string) bool {
		if bundle == nil || db == nil || task == nil || strings.TrimSpace(actorOpenID) == "" {
			return false
		}
		role, ok := chain.FindRoleOfNode(bundle, task.NodeID)
		if !ok {
			return false // 节点角色不可判 ⇒ 不放行（fail-closed）
		}
		ag, err := db.FindActiveRoleAgent(ctx, role)
		if err != nil || ag == nil {
			return false // 无 active 代理 / 查询失败 ⇒ 不放行
		}
		if strings.TrimSpace(ag.AgentOpenID) != actorOpenID {
			return false // actor 不是该角色的在册代理
		}
		allowed, _ := chain.NodeAllowsAgent(bundle, task.NodeID)
		return allowed
	}
}
