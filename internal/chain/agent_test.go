package chain

// N-028 链侧：备付金节点按「节点」排除代理人（不按角色 —— 误拦初审与漏拦同样致命）。

import (
	"testing"
)

func TestNodeAllowsAgent(t *testing.T) {
	b := loadBundle(t)

	// 应拦：备付金两节点（制度「备付金审批不得代理」）
	for _, id := range []string{"approve_petty_cash", "disburse"} {
		allowed, reason := NodeAllowsAgent(b, id)
		if allowed {
			t.Errorf("节点 %s 应拒绝代理人，却放行", id)
		}
		if reason == "" {
			t.Errorf("节点 %s 拒绝时应给出可见 reason", id)
		}
	}

	// 应放行：ops_supervisor 在采二档的初审节点 —— 按角色一刀切禁 ops_supervisor 才是误伤
	for _, id := range []string{"ops_review", "supervisor_approval", "submit_group"} {
		if allowed, reason := NodeAllowsAgent(b, id); !allowed {
			t.Errorf("节点 %s 应允许代理人，却拒绝：%s", id, reason)
		}
	}

	// 动态读 spec：denied 集合来自 tier1 节点表，非硬编码常量
	denied := PettyCashAgentDeniedNodeIDs(b)
	if len(denied) != 2 || !denied["approve_petty_cash"] || !denied["disburse"] {
		t.Errorf("PettyCashAgentDeniedNodeIDs = %v（应恰为 tier1 的两个备付金节点）", denied)
	}
}
