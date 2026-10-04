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

func int64Ptr(v int64) *int64 { return &v }

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

// backfillApprovals 取 backfill_approval 节点产出的审批角色（SourceNodeID 前缀）。
func backfillApprovals(nodes []RoleNode) []string {
	out := []string{}
	for _, n := range nodes {
		if n.IsApproval && strings.HasPrefix(n.SourceNodeID, "backfill_approval_") {
			out = append(out, strings.TrimPrefix(n.SourceNodeID, "backfill_approval_"))
		}
	}
	return out
}

// TestTierExpandEmergencyMax N-045 · T2 展开对照表（emergency.backfill_approval）。
// tier_source=emergency_max（R-31 跨单就高，两值必须齐全）；
// exclude_roles=[supervisor, ops_supervisor]（去重：seq2 紧急认定 / seq6 核销闭合已签）。
func TestTierExpandEmergencyMax(t *testing.T) {
	b := mustBundle(t)
	cases := []struct {
		name      string
		amount    *int64 // 补录金额
		related   *int64 // 关联 PR 金额
		wantRoles []string
		wantErr   bool
	}{
		{"采一档就高800元⇒展开为空（ops 已在 seq6 签，去重只签一次）",
			int64Ptr(80000), int64Ptr(60000), []string{}, false},
		{"采二档就高5000元（PR 侧）⇒仅 pgm（sup 已在 seq2 剔除）",
			int64Ptr(300000), int64Ptr(500000), []string{"project_general_manager"}, false},
		{"采三档就高30000元（补录侧）⇒仅 pgm",
			int64Ptr(3000000), int64Ptr(300000), []string{"project_general_manager"}, false},
		{"★ 缺关联 PR 金额 ⇒ 可见失败（不单边退化、不静默降档）",
			int64Ptr(300000), nil, nil, true},
		{"★ 缺补录金额 ⇒ 可见失败（对称双向）",
			nil, int64Ptr(500000), nil, true},
		{"★ 变异B判别行：补录500元/PR 6000元 就高档3⇒pgm（只取单边会降档到 tier1⇒空）",
			int64Ptr(50000), int64Ptr(600000), []string{"project_general_manager"}, false},
		// ★ N-049 ③（我方补）：**补录侧更大且跨档**的「就高」正例 —— 上表第 3 行虽也是
		// 补录侧更大，但两值分属采三/采二档、**两条链去重后同为 pgm** ⇒ 对「就高」无鉴别力
		// （取 min / 只取 PR 侧都仍得 pgm）。本行的两个取值**分别落在采三档与采一档**：
		// · 应然（就高）＝档3 ⇒ [supervisor, pgm] 去 supervisor ⇒ pgm；
		// · 若误取关联 PR 单边（档1）⇒ [ops_supervisor] 被 exclude 剔空 ⇒ 空。
		// ⇒ 与变异B判别行（PR 侧更大）**互为镜像**，两条一起把「就高」两侧都钉住。
		{"★ 变异C判别行（N-049 ③）：补录6000元(档3)/PR 500元(档1) ⇒ pgm（只取 PR 单边会降到采一档⇒空）",
			int64Ptr(600000), int64Ptr(50000), []string{"project_general_manager"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes, err := BuildNodes(b, "emergency", Facts{
				DocType:              "BA",
				AmountCents:          c.amount,
				RelatedPRAmountCents: c.related,
			})
			if c.wantErr {
				if err == nil {
					t.Fatalf("缺值应可见失败，实为成功（展开=%v）", backfillApprovals(nodes))
				}
				if !strings.Contains(err.Error(), "emergency_max") {
					t.Errorf("错误文案应点名 tier_source=emergency_max：%v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildNodes: %v", err)
			}
			got := backfillApprovals(nodes)
			if len(got) != len(c.wantRoles) {
				t.Fatalf("backfill_approval 展开 = %v, 期望 %v（全链审批=%v）", got, c.wantRoles, approvalRolesOf(nodes))
			}
			for i := range got {
				if got[i] != c.wantRoles[i] {
					t.Errorf("展开第 %d 项 = %s, 期望 %s", i, got[i], c.wantRoles[i])
				}
			}
		})
	}
}

// TestTierExpandEmergencyEmptyIsExpected N-045 · T2「采一档⇒空」显式用例：
// 展开为空是**预期结果**（零产出＝去重口径，非又惰性了）—— 断言：不报错、
// backfill_approval 无审批任务、但链上其余审批任务照常生成（证明确实跑过展开路径）。
func TestTierExpandEmergencyEmptyIsExpected(t *testing.T) {
	b := mustBundle(t)
	amt, rel := int64(80000), int64(60000)
	nodes, err := BuildNodes(b, "emergency", Facts{DocType: "BA", AmountCents: &amt, RelatedPRAmountCents: &rel})
	if err != nil {
		t.Fatalf("采一档展开不应报错（空是预期）: %v", err)
	}
	if got := backfillApprovals(nodes); len(got) != 0 {
		t.Fatalf("采一档（链=[ops] 且 ops 被 exclude）应展开为空, 实得 %v", got)
	}
	// 链上其余审批任务仍在 ⇒ 证明不是「整链没跑」：
	// emergency_confirm(supervisor) 与 close_loop(ops_supervisor) 各生成一个。
	others := []string{}
	for _, n := range nodes {
		if n.IsApproval && !strings.HasPrefix(n.SourceNodeID, "backfill_approval_") {
			others = append(others, n.SourceNodeID)
		}
	}
	if len(others) < 2 {
		t.Errorf("链上其余审批任务应照常生成（证明确实跑过展开路径），实得 %v", others)
	}
}
