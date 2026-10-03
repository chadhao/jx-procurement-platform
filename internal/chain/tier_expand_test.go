package chain

// N-044 · tier_expand 展开矩阵（T1 SS / T2 PC）——
// 对照任务包 §1/§2 的展开结果表；含 exclude_roles 去重与 r15_max 就高。
// SourceNodeID 断言＝`<节点id>_<role>`（T3 自定命名，可由 spec 派生、零角色字面量在 ID 里）。

import (
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func mustBundle(t *testing.T) *specload.Bundle {
	t.Helper()
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatalf("spec 加载失败: %v", err)
	}
	return b
}

// approvalRolesOf 提取链上审批任务的角色序列（按 Seq 序）。
func approvalRolesOf(nodes []RoleNode) []string {
	out := []string{}
	for _, n := range nodes {
		if n.IsApproval {
			out = append(out, n.ActorRole)
		}
	}
	return out
}

func hasApprovalRole(nodes []RoleNode, role string) bool {
	for _, r := range approvalRolesOf(nodes) {
		if r == role {
			return true
		}
	}
	return false
}

// TestTierExpandSS T1：sole_source.tier_chain 按档位展开（amount_cents）＋ exclude_roles 剔 PGM。
func TestTierExpandSS(t *testing.T) {
	b := mustBundle(t)
	cases := []struct {
		name      string
		amount    int64
		wantTier  string
		wantRoles []string // 按序（审批任务角色）
	}{
		{"999元采一档→ops_supervisor", 99900, "purchase_tier1", []string{"inspector_group", "ops_supervisor", "project_general_manager", "ops_supervisor"}},
		{"1000元采二档→supervisor（剔PGM）", 100000, "purchase_tier2", []string{"inspector_group", "supervisor", "project_general_manager", "ops_supervisor"}},
		{"6000元采三档→supervisor（剔PGM）", 600000, "purchase_tier3", []string{"inspector_group", "supervisor", "project_general_manager", "ops_supervisor"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			amt := c.amount
			nodes, err := BuildNodes(b, "sole_source", Facts{DocType: "SS", AmountCents: &amt})
			if err != nil {
				t.Fatalf("BuildNodes: %v", err)
			}
			got := approvalRolesOf(nodes)
			if len(got) != len(c.wantRoles) {
				t.Fatalf("审批角色 = %v, 期望 %v（展开后任务数不符）", got, c.wantRoles)
			}
			for i := range got {
				if got[i] != c.wantRoles[i] {
					t.Errorf("第 %d 个审批角色 = %s, 期望 %s（全序：%v）", i, got[i], c.wantRoles[i], got)
				}
			}
			// ★ exclude_roles＝PGM 不得出现在 tier_chain 段（SS 全链 PGM 签字点唯一＝pgm_final）
			//   采二/三档：tier_chain 展开不得含 PGM；pgm_final 仍保留（整链恰一个 PGM）
			if c.wantTier == "purchase_tier2" || c.wantTier == "purchase_tier3" {
				pgmCount := 0
				for _, r := range got {
					if r == "project_general_manager" {
						pgmCount++
					}
				}
				if pgmCount != 1 {
					t.Errorf("PGM 签字点 = %d, 期望 1（exclude_roles 去重 + pgm_final 唯一终审）: %v", pgmCount, got)
				}
			}
			// SourceNodeID：tier_chain 展开项 = <节点id>_<role>（T3 命名规则，可派生）
			for _, n := range nodes {
				if strings.HasPrefix(n.SourceNodeID, "tier_chain_") {
					role := strings.TrimPrefix(n.SourceNodeID, "tier_chain_")
					if !approverRoles[role] {
						t.Errorf("展开节点 %q 的 role 不是合法 approverRoles key（命名应可派生）", n.SourceNodeID)
					}
				}
			}
		})
	}
}

// TestTierExpandPC T2：change.tier_approval 按 r15_max 就高展开（无 exclude ⇒ sup+pgm）。
func TestTierExpandPC(t *testing.T) {
	b := mustBundle(t)
	cases := []struct {
		name     string
		change   int64
		original int64
		want     []string // tier_chain 段的展开角色（tier_approval 段）
	}{
		{"就高档tier1→仅ops", 50000, 40000, []string{"ops_supervisor"}},
		{"就高档tier2→sup+pgm", 100000, 500000, []string{"supervisor", "project_general_manager"}},
		{"就高档tier3→sup+pgm", 600000, 500001, []string{"supervisor", "project_general_manager"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			change, orig := c.change, c.original
			nodes, err := BuildNodes(b, "change", Facts{
				DocType:                     "PC",
				ChangeAmountCents:           &change,
				OriginalContractAmountCents: &orig,
			})
			if err != nil {
				t.Fatalf("BuildNodes: %v", err)
			}
			// tier_approval 展开段：SourceNodeID 前缀 tier_approval_
			got := []string{}
			for _, n := range nodes {
				if n.IsApproval && strings.HasPrefix(n.SourceNodeID, "tier_approval_") {
					got = append(got, strings.TrimPrefix(n.SourceNodeID, "tier_approval_"))
				}
			}
			if len(got) != len(c.want) {
				t.Fatalf("tier_approval 展开 = %v, 期望 %v（全链审批=%v）", got, c.want, approvalRolesOf(nodes))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("展开第 %d 项 = %s, 期望 %s", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestTierExpandSingleSideFallback T2 前置依赖处置：original 未注入（preview 形态）时
// r15_max 退化为**可得侧/顶层金额**的单值 fallback（任务包授权、非静默）；全缺 ⇒ 可见错误。
func TestTierExpandSingleSideFallback(t *testing.T) {
	b := mustBundle(t)
	// ① 只有 change（original 未注入）⇒ 就高在可得集上退化单边：TierOf(change)
	change := int64(600000)
	nodes, err := BuildNodes(b, "change", Facts{DocType: "PC", ChangeAmountCents: &change})
	if err != nil {
		t.Fatalf("单边 change 应可展开: %v", err)
	}
	if !hasApprovalRole(nodes, "project_general_manager") {
		t.Errorf("600000 单边应按 tier3 展开 sup+pgm, 实际审批=%v", approvalRolesOf(nodes))
	}
	// ② 两值全缺但顶层 amount 兜底（preview 形态）⇒ 按 AmountCents 定档
	amt := int64(50000)
	nodes2, err := BuildNodes(b, "change", Facts{DocType: "PC", AmountCents: &amt})
	if err != nil {
		t.Fatalf("顶层兜底应可展开: %v", err)
	}
	if !hasApprovalRole(nodes2, "ops_supervisor") {
		t.Errorf("50000 顶层兜底应按 tier1 展开 ops, 实际=%v", approvalRolesOf(nodes2))
	}
	// ③ 全缺 ⇒ 可见错误（不静默假定）
	if _, err := BuildNodes(b, "change", Facts{DocType: "PC"}); err == nil {
		t.Fatal("三金额全缺应可见失败（不静默假定档位）")
	}
}
