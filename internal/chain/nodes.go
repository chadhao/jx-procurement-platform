package chain

import (
	"fmt"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// 审批角色（chain.json roles 的 key）—— 这些 actor 生成 flow 审批任务。
var approverRoles = map[string]bool{
	"supervisor":              true,
	"project_general_manager": true,
	"deputy_general_manager":  true,
	"ops_supervisor":          true,
	"inspector_group":         true,
}

// tier3_plus 阈值（分）—— 锚定 chain.json#routes.purchase_tier3.branches.tier3_plus.when
// "amount_cents > 50000000 || is_fixed_asset == true"。
// ★ 两个触发条件**均已可达**（N-013 已 AGREED：forms/PR.json 已补 is_fixed_asset，
//
//	submit/preview 从表单取值传入 Facts.IsFixedAsset）；金额常量与 spec 字面量
//	由锚定测试互锁，spec 漂移即测试红。
const tier3PlusAmountCents int64 = 50000000 // ★ 2026-10-09 制度修正：50 万元（原 20 万）

// BuildNodes 展开指定流程线为角色级链（含非审批环节）。
//
// 输入 routeID 必须是 ResolveRoute 的结果（不在此再判线）。
// 输出 Seq 为审批任务序（1..N，IsApproval=false 的环节 Seq=0）。
func BuildNodes(b *specload.Bundle, routeID string, f Facts) ([]RoleNode, error) {
	// ★ N-056（A8）登记型：RouteID == "" ＝ 链为空 —— 零节点（必须在 Routes 查表之前，
	//   否则会落 ErrRouteMissing；ResolveRoute 对 no_approval_chain 单据返回空串不报错）。
	if routeID == "" {
		return []RoleNode{}, nil
	}
	// ---- 合同统一两级（CT 单据 / T4）：不走 routes 表，直接由 contract_approval.order 展开 ----
	//   审批任务＝supervisor_approval + pgm_approval（沿用 contract_supervisor/pgm 节点 id，
	//   与 expandContract 同源 ⇒ R-26 的通用插入会因 hasContractNodes 命中而不再重复插入）；
	//   其余步骤（确认/拟稿/上传/签署）为动作环节，进展示不生成审批任务。
	if routeID == b.Chain.ContractApproval.ID {
		return buildContractRouteNodes(b, f), nil
	}
	route, ok := b.Chain.Routes[routeID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrRouteMissing, routeID)
	}

	nodes := []RoleNode{}
	// ★ Seq **后置分配**（T3）：结构全部建完（含 R-26 合同节点插入）后统一编号，
	//   插入中段不再造成序号碰撞（原「边建边编」在合同触发插入时会与后续节点撞号）。
	appendApproval := func(sourceID, name, role, note string) {
		nodes = append(nodes, RoleNode{
			SourceNodeID: sourceID, NodeName: name,
			ActorRole: role, IsApproval: true, BranchNote: note,
		})
	}
	appendAction := func(sourceID, name, note string) {
		nodes = append(nodes, RoleNode{SourceNodeID: sourceID, NodeName: name, IsApproval: false, BranchNote: note})
	}

	// tier3_plus：在「询比价」之前插入加强节点（R-09；节点顺序待用户确认，按 spec 先行）。
	insertBefore := ""
	plusBranch := route.Branches["tier3_plus"]
	if routeID == "purchase_tier3" && plusBranch.InsertBefore != "" &&
		(f.AmountCents != nil && *f.AmountCents > tier3PlusAmountCents || f.IsFixedAsset) {
		insertBefore = plusBranch.InsertBefore
	}

	for _, n := range route.Nodes {
		// ---- tier3_plus 插入节点 ----
		if insertBefore != "" && n.ID == insertBefore {
			for _, ins := range plusBranch.InsertedNodes {
				if approverRoles[ins.Actor] {
					appendApproval(ins.ID, ins.Label, ins.Actor, "tier3_plus 插入（R-09）")
				} else {
					appendAction(ins.ID, ins.Label, "tier3_plus 插入（R-09）")
				}
			}
			insertBefore = "" // 只插一次
		}

		// ---- 合同 ref 展开 ----
		if n.Ref == "contract_two_level" {
			nodes = append(nodes, expandContract(b, f)...)
			continue
		}

		// ---- 条件分支：R-03 备付金节点上抬（N-057 ② 数据驱动）----
		//   绑定键＝branches[*].id（稳定标识可入代码）；when 只是人读描述（真值＝
		//   Facts.ApplicantIsOpsSupervisor）；actor 从 spec 取（唯一来源、零字面量）。
		//   fail-visible：命中事实但分支缺失 / actor 不可用 ⇒ 可见错误（不静默降级）。
		if n.ID == "approve_petty_cash" {
			role := "ops_supervisor"
			note := ""
			if f.ApplicantIsOpsSupervisor {
				br, ok := branchByID(n, "applicant_is_ops_supervisor")
				if !ok {
					return nil, fmt.Errorf(
						"chain: 节点 %s 的事实 applicant_is_ops_supervisor=true 但缺少分支 applicant_is_ops_supervisor（spec 分支声明缺失 —— 不静默降级）",
						n.ID)
				}
				if br.Actor == "" || !approverRoles[br.Actor] {
					return nil, fmt.Errorf(
						"chain: 分支 %s 的 actor=%q 不可用（须为审批角色）—— 不静默保留默认角色",
						br.ID, br.Actor)
				}
				role = br.Actor
				note = "R-03：申请人＝综合运营主管，本节点上抬至主管领导"
			}
			appendApproval(n.ID, n.Label, role, note)
			continue
		}

		// ---- N-044 档位审批链展开（tier_expand 取代复合 actor 与伪 ref） ----
		//   required:true 的 tier_chain/tier_approval 此前落入「未知 actor」⇒ 不生成任务
		//   （惰性必需节点）；现按 spec 的 tier_expand 逐角色 appendApproval。
		if n.TierExpand != nil && n.TierExpand.Kind == "approval_chain" {
			if err := expandTierApproval(b, f, n, appendApproval); err != nil {
				return nil, err
			}
			continue
		}

		// ---- 一般节点 ----
		// ★ N-065 T2（conventions.node_task_generation ☆ 显式优先）：
		//   generates_task 显式声明 > isActionActor 缺省推导；键缺失 ⇒ 行为与既往完全一致
		//   （既有 9 条流程线零变化）。true ⇒ 该节点必须生成待办（哪怕 actor 是动作型，
		//   如 return_receipt 的 applicant —— 办理人在 Resolve 侧解析为申请人本人）。
		if n.GeneratesTask != nil {
			if *n.GeneratesTask {
				appendApproval(n.ID, n.Label, n.Actor, "generates_task 显式声明（conventions.node_task_generation）")
			} else {
				appendAction(n.ID, n.Label, "generates_task: false 显式声明（不生成待办）")
			}
			continue
		}
		if approverRoles[n.Actor] {
			appendApproval(n.ID, n.Label, n.Actor, "")
			continue
		}
		if isActionActor(n.Actor) {
			appendAction(n.ID, n.Label, "")
			continue
		}
		// 未知/复合 actor（如 "supervisor → project_general_manager"）——保守不生成任务。
		appendAction(n.ID, n.Label, fmt.Sprintf("actor=%q 未映射为审批任务", n.Actor))
	}
	// ---- T3 / R-26 corollary：有合同 ⇒ 统一两级合同审批（不因金额减免） ----
	//   route 自带合同 ref 的（tier2/tier3）已展开；tier1 与费用线**无合同步骤** ——
	//   `has_contract=true` 时在首个审批节点之后插入合同两级（boundary_decided_by_us：
	//   「<1,000 元但签了合同 ⇒ 公户付款 **且** 合同审批两级照走」；contract_approval.trigger
	//   同口径：触发条件＝是否签合同，不是金额）。
	if f.HasContract && !hasContractNodes(nodes) {
		exp := expandContract(b, f)
		for i := range exp {
			exp[i].BranchNote = joinBranchNote(exp[i].BranchNote,
				"R-26：有合同 ⇒ 合同审批两级照走（触发条件＝是否签合同，非金额）")
		}
		pos := -1
		for i, n := range nodes {
			if n.IsApproval {
				pos = i
				break
			}
		}
		if pos >= 0 {
			rebuilt := make([]RoleNode, 0, len(nodes)+len(exp))
			rebuilt = append(rebuilt, nodes[:pos+1]...) // 首个审批节点（含）之前原样保留
			rebuilt = append(rebuilt, exp...)           // 合同两级紧跟其后
			rebuilt = append(rebuilt, nodes[pos+1:]...)
			nodes = rebuilt
		} else {
			nodes = append(nodes, exp...)
		}
	}

	// Seq 后置统一编号（仅审批节点 1..N）。
	seq := 0
	for i := range nodes {
		if nodes[i].IsApproval {
			seq++
			nodes[i].Seq = seq
		}
	}
	return nodes, nil
}

// expandTierApproval 按节点的 tier_expand 展开档位审批链（N-044 · T1/T2）。
//
//	步骤：① tier_source 解析档位 → ② 取 thresholds.purchase.bands[id].approval_chain
//	      → ③ 剔除 exclude_roles（用户口径：重复签批人只签一次）
//	      → ④ 逐角色 appendApproval（复用既有范式）。
//
// ★ SourceNodeID 命名（T3，自定）：`<节点id>_<role>`（如 tier_chain_supervisor）——
//
//	与 expandContract 的 contract_supervisor/contract_pgm 同族、**可由 spec 派生**
//
// （节点 id 与角色 key 均来自 spec，代码零字面量）；冒号/点号不用（task_id
//
//	格式 {biz}-{node}-{assignee}-{round}-{seq} 下划线最稳）。
//
// ★ 与 flow 任务、nodeFieldSpecFor 的关系：展开节点 id 形如 tier_chain_supervisor，
//
//	**不等于**既有规则锚定的 tech_opinion/pgm_final/ledger_submit ⇒ 不误伤
//
// （本批不给展开节点加必填字段）。
func expandTierApproval(b *specload.Bundle, f Facts, n specload.NodeDoc,
	appendApproval func(sourceID, name, role, note string)) error {
	if n.TierExpand == nil {
		return nil
	}
	tierID, err := resolveTierForExpand(b, f, n.TierExpand.TierSource)
	if err != nil {
		return fmt.Errorf("%w: 节点 %s 档位解析失败: %s", ErrAmountMissing, n.ID, err)
	}
	chainRoles := bandApprovalChain(b, tierID)
	if len(chainRoles) == 0 {
		return fmt.Errorf("%w: 档位 %s 的 approval_chain 为空（spec thresholds 缺声明）", ErrRouteMissing, tierID)
	}
	exclude := map[string]bool{}
	for _, r := range n.TierExpand.ExcludeRoles {
		exclude[r] = true
	}
	note := fmt.Sprintf("tier_expand（N-044）：档位 %s", tierID)
	for _, role := range chainRoles {
		if exclude[role] {
			continue
		}
		appendApproval(n.ID+"_"+role, n.Label, role, note)
	}
	return nil
}

// resolveTierForExpand tier_source → 档位 id。
//
//	amount_cents：同 chain.TierOf（缺金额 ⇒ 可见错误）。
//	r15_max：R-15 就高 tier_of(max(change, original))；★ 两值齐全取 max；
//	★ 仅单边可得 ⇒ 取可得侧（就高在可得集上退化 —— preview 时 original 常未注入，
//	此为任务包 T2 授权的「如实单值 fallback」，非静默假定）；★ 全缺 ⇒ 用 AmountCents
//	兜底（preview 顶层金额），仍缺 ⇒ 可见错误。
func resolveTierForExpand(b *specload.Bundle, f Facts, source string) (string, error) {
	switch source {
	case "amount_cents":
		if f.AmountCents == nil {
			return "", fmt.Errorf("%w: tier_source=amount_cents 需要金额", ErrAmountMissing)
		}
		return TierOf(b, *f.AmountCents)
	case "r15_max":
		var candidates []int64
		if f.ChangeAmountCents != nil {
			candidates = append(candidates, *f.ChangeAmountCents)
		}
		if f.OriginalContractAmountCents != nil {
			candidates = append(candidates, *f.OriginalContractAmountCents)
		}
		if len(candidates) == 0 && f.AmountCents != nil {
			// 单值 fallback（任务包 T2 授权）：preview 两值均未注入时退回顶层金额。
			candidates = append(candidates, *f.AmountCents)
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("%w: tier_source=r15_max 无任何金额可就高（change/original/amount 均缺）", ErrAmountMissing)
		}
		maxV := candidates[0]
		for _, v := range candidates[1:] {
			if v > maxV {
				maxV = v
			}
		}
		return TierOf(b, maxV)
	case "emergency_max":
		// R-31 跨单就高 tier_of(max(补录金额, 关联 PR 金额))。
		// ★ 两值必须齐全（conventions.tier_source ③——与 r15_max 的单边退化刻意不同）：
		// 「不得降档」是单调上界，单边取值可能低于应属档位 ⇒ 缺任一即可见失败。
		if f.AmountCents == nil {
			return "", fmt.Errorf("%w: tier_source=emergency_max 缺补录金额（Facts.AmountCents）", ErrAmountMissing)
		}
		if f.RelatedPRAmountCents == nil {
			return "", fmt.Errorf("%w: tier_source=emergency_max 缺关联 PR 金额（Facts.RelatedPRAmountCents）", ErrAmountMissing)
		}
		maxV := *f.AmountCents
		if *f.RelatedPRAmountCents > maxV {
			maxV = *f.RelatedPRAmountCents
		}
		return TierOf(b, maxV)
	default:
		return "", fmt.Errorf("%w: 未知 tier_source %q", ErrRouteMissing, source)
	}
}

// bandApprovalChain 取指定档位的审批链（spec thresholds.purchase.bands[].approval_chain）。
func bandApprovalChain(b *specload.Bundle, tierID string) []string {
	for _, band := range b.Chain.Thresholds.Purchase.Bands {
		if band.ID == tierID {
			return band.ApprovalChain
		}
	}
	return nil
}

// hasContractNodes 链上是否已含合同两级节点。
func hasContractNodes(nodes []RoleNode) bool {
	for _, n := range nodes {
		if n.SourceNodeID == "contract_supervisor" || n.SourceNodeID == "contract_pgm" {
			return true
		}
	}
	return false
}

// joinBranchNote 拼接分支说明。
func joinBranchNote(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "；" + b
}

// expandContract 展开合同统一两级（chain.contract_approval）。
// ★ 只取 supervisor_approval 与 pgm_approval 两个 id —— 制度「合同审批统一两级」；
//
//	draft_contract / upload_contract（经办人动作）、sign（签署动作）、
//	confirm_supplier（与 tier3 的 pgm_confirm 重复）均不生成审批任务。
//
// ★ supervisor_is_pgm 分支：申请人部门 ∈ project_general_manager.is_also_supervisor_for
//
//	⇒ 一级即终审，跳过二级（chain.json contract_approval.branches.supervisor_is_pgm）。
func expandContract(b *specload.Bundle, f Facts) []RoleNode {
	out := []RoleNode{}
	// 分支判定：机器可读数组（roles.project_general_manager.is_also_supervisor_for）
	pgm := b.Chain.Roles["project_general_manager"]
	supervisorIsPGM := false
	for _, d := range pgm.IsAlsoSupervisorFor {
		if d == f.Department {
			supervisorIsPGM = true
			break
		}
	}

	appendApproval := func(sourceID, name, role, note string) {
		out = append(out, RoleNode{
			SourceNodeID: sourceID, NodeName: name,
			ActorRole: role, IsApproval: true, BranchNote: note,
		})
	}

	appendApproval("contract_supervisor", "合同审批·一级（主管领导）", "supervisor", "")
	if supervisorIsPGM {
		out = append(out, RoleNode{
			SourceNodeID: "contract_pgm", NodeName: "合同审批·二级（项目总经理）",
			IsApproval: false, BranchNote: "supervisor_is_pgm：主管领导即项目总经理，一级即终审，跳过二级",
		})
	} else {
		appendApproval("contract_pgm", "合同审批·二级（项目总经理）", "project_general_manager", "")
	}
	return out
}

// branchByID 按稳定绑定键 branches[*].id 顺序查找分支（N-057 ②）。
// ★ 已取代 hasBranchWhen（比对 when 文本 —— 文本一改分支静默失效，正是本契约动因；
// grep 确认全仓除定义外仅 R-03 段一处引用 ⇒ 随数据驱动改造一并删除，不留死代码）。
func branchByID(n specload.NodeDoc, id string) (specload.NodeBranch, bool) {
	for _, br := range n.Branches {
		if br.ID == id {
			return br, true
		}
	}
	return specload.NodeBranch{}, false
}

// isActionActor 动作/系统环节的 actor —— 不生成审批任务。
func isActionActor(actor string) bool {
	switch actor {
	case "applicant", "system", "purchaser":
		return true
	}
	return false
}

// buildContractRouteNodes 由 contract_approval 展开 CT 的两级合同链（T4）。
func buildContractRouteNodes(b *specload.Bundle, f Facts) []RoleNode {
	out := []RoleNode{}
	pgm := b.Chain.Roles["project_general_manager"]
	supervisorIsPGM := containsDept(pgm.IsAlsoSupervisorFor, f.Department)
	for _, step := range b.Chain.ContractApproval.Order {
		switch step.ID {
		case "supervisor_approval":
			out = append(out, RoleNode{SourceNodeID: "contract_supervisor", NodeName: step.Label,
				ActorRole: "supervisor", IsApproval: true})
		case "pgm_approval":
			if supervisorIsPGM {
				out = append(out, RoleNode{SourceNodeID: "contract_pgm", NodeName: step.Label,
					IsApproval: false,
					BranchNote: "supervisor_is_pgm：主管领导即项目总经理，一级即终审，跳过二级"})
				continue
			}
			out = append(out, RoleNode{SourceNodeID: "contract_pgm", NodeName: step.Label,
				ActorRole: "project_general_manager", IsApproval: true})
		default:
			out = append(out, RoleNode{SourceNodeID: step.ID, NodeName: step.Label, IsApproval: false})
		}
	}
	seq := 0
	for i := range out {
		if out[i].IsApproval {
			seq++
			out[i].Seq = seq
		}
	}
	return out
}
