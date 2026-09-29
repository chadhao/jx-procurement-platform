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
// "amount_cents > 20000000 || is_fixed_asset == true"。
// ★ 两个触发条件**均已可达**（N-013 已 AGREED：forms/PR.json 已补 is_fixed_asset，
//
//	submit/preview 从表单取值传入 Facts.IsFixedAsset）；金额常量与 spec 字面量
//	由锚定测试互锁，spec 漂移即测试红。
const tier3PlusAmountCents int64 = 20000000

// BuildNodes 展开指定流程线为角色级链（含非审批环节）。
//
// 输入 routeID 必须是 ResolveRoute 的结果（不在此再判线）。
// 输出 Seq 为审批任务序（1..N，IsApproval=false 的环节 Seq=0）。
func BuildNodes(b *specload.Bundle, routeID string, f Facts) ([]RoleNode, error) {
	route, ok := b.Chain.Routes[routeID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrRouteMissing, routeID)
	}

	nodes := []RoleNode{}
	approvalSeq := 0
	appendApproval := func(sourceID, name, role, note string) {
		approvalSeq++
		nodes = append(nodes, RoleNode{
			Seq: approvalSeq, SourceNodeID: sourceID, NodeName: name,
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
			nodes = append(nodes, expandContract(b, f, &approvalSeq)...)
			continue
		}

		// ---- 条件分支：R-03 备付金节点上抬 ----
		if n.ID == "approve_petty_cash" {
			role := "ops_supervisor"
			note := ""
			if f.ApplicantIsOpsSupervisor && hasBranchWhen(n, "applicant.is_ops_supervisor == true") {
				role = "supervisor"
				note = "R-03：申请人＝综合运营主管，本节点上抬至主管领导"
			}
			appendApproval(n.ID, n.Label, role, note)
			continue
		}

		// ---- 一般节点 ----
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
	return nodes, nil
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
func expandContract(b *specload.Bundle, f Facts, approvalSeq *int) []RoleNode {
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
		*approvalSeq++
		out = append(out, RoleNode{
			Seq: *approvalSeq, SourceNodeID: sourceID, NodeName: name,
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

// hasBranchWhen 节点是否声明了指定 when 的条件分支。
func hasBranchWhen(n specload.NodeDoc, when string) bool {
	for _, br := range n.Branches {
		if br.When == when {
			return true
		}
	}
	return false
}

// isActionActor 动作/系统环节的 actor —— 不生成审批任务。
func isActionActor(actor string) bool {
	switch actor {
	case "applicant", "system", "purchaser":
		return true
	}
	return false
}
