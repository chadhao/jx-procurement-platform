package httpapi

// POST /api/approval/preview —— 分档/审批链预览（M7 / D5：与 submit **共算同一入口**，
// 前端不得本地算分档）。纯本地计算：不推飞书、不烧配额、不落库。
//
// 契约（批 1 方案 d4）：
//   请求 {doc_type, amount_cents?, usage_category_l1?, usage_category_l2?,
//         payment_method_input?, department?}   —— department 缺省取会话部门
//   响应 {spec_version, tier, route{id,label,env_count,payment},
//         nodes[{seq,source_node_id,node_name,actor_role,is_approval,branch_note,
//                approvers[{open_id,name}], resolved}],
//         unresolved_roles[{node_id,node_name,role,reason}]}
//   ★ unresolved 非空 ⇒ 提交将被阻断（FR-M9-02），预览**可见暴露**缺人（R-g 缓解）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/chain"
)

type previewRequest struct {
	DocType            string `json:"doc_type"`
	AmountCents        *int64 `json:"amount_cents"`
	UsageCategoryL1    string `json:"usage_category_l1"`
	UsageCategoryL2    string `json:"usage_category_l2"`
	PaymentMethodInput string `json:"payment_method_input"`
	Department         string `json:"department"`
	// IsFixedAsset N-013：PR 固定资产勾选 → tier3_plus "or is_fixed_asset" 分支。
	IsFixedAsset bool `json:"is_fixed_asset"`
	// HasContract T3/R-26：付款路径第 1 判据 + 合同两级触发（CT 恒 true）。
	HasContract bool `json:"has_contract"`
}

type previewNode struct {
	Seq          int               `json:"seq"`
	SourceNodeID string            `json:"source_node_id"`
	NodeName     string            `json:"node_name"`
	ActorRole    string            `json:"actor_role"`
	IsApproval   bool              `json:"is_approval"`
	BranchNote   string            `json:"branch_note"`
	Approvers    []previewApprover `json:"approvers"`
	Resolved     bool              `json:"resolved"`
	// CoSignCount 审批人数量（N-014：≥2 ⇒ 全员会签；preview 必须显式标注「N 人会签」）。
	CoSignCount int `json:"co_sign_count"`
}

type previewApprover struct {
	OpenID string `json:"open_id"`
	Name   string `json:"name"`
}

type previewResponse struct {
	SpecVersion     string                 `json:"spec_version"`
	Tier            string                 `json:"tier"`
	Route           previewRoute           `json:"route"`
	Nodes           []previewNode          `json:"nodes"`
	UnresolvedRoles []chain.UnresolvedRole `json:"unresolved_roles"`
	// Warnings 非阻断告警（N-014：会签人数 ≥ warn_threshold 时必须提示，但不拦业务）.
	Warnings []string `json:"warnings"`
	// PaymentRoute 付款路径（T3/R-26：group_public_account / petty_cash / personal_advance_reimburse）。
	PaymentRoute string `json:"payment_route"`
}

type previewRoute struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	EnvCount int    `json:"env_count"`
	Payment  string `json:"payment"`
}

func (d Deps) handleApprovalPreview(c echo.Context) error {
	if d.Spec == nil || d.Chain == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "机读规格/链计算未装配")
	}
	idn, ur, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	applicantName := ""
	if ur != nil {
		applicantName = ur.Name
	}
	var req previewRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if strings.TrimSpace(req.DocType) == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "doc_type 不能为空")
	}
	ctx := c.Request().Context()
	facts := chain.Facts{
		DocType:                  req.DocType,
		AmountCents:              req.AmountCents,
		UsageCategoryL1:          req.UsageCategoryL1,
		PaymentMethodInput:       req.PaymentMethodInput,
		Department:               firstNonEmptyStr(req.Department, idn.Department),
		ApplicantIsOpsSupervisor: idn.Role == "综合运营主管",
		IsFixedAsset:             req.IsFixedAsset,                       // N-013
		HasContract:              req.HasContract || req.DocType == "CT", // T3/R-26
		// N-065 T2：与提交侧同源（generates_task 显式 applicant 待办）——
		// 缺此两值 ⇒ preview 对 BA 会显示 unresolved（误导），故同批传入。
		ApplicantOpenID: idn.OpenID,
		ApplicantName:   applicantName,
	}
	paymentRoute, pErr := chain.PaymentRouteOf(d.Spec, facts)
	if pErr != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, pErr.Error())
	}
	rc, err := d.Chain.Compute(ctx, facts)
	if err != nil {
		if isChainInputError(err) {
			return fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	routeDef := d.Spec.Chain.Routes[rc.Route.RouteID]
	// 审批人按 source_node_id 索引（Spec 由 Resolve 产出，Seq 对应审批任务）
	approversByNode := map[string][]previewApprover{}
	resolvedNodes := map[string]bool{}
	for _, n := range rc.Spec {
		approversByNode[n.NodeID] = make([]previewApprover, 0, len(n.Approvers))
		for _, a := range n.Approvers {
			approversByNode[n.NodeID] = append(approversByNode[n.NodeID], previewApprover{OpenID: a.OpenID, Name: a.Name})
		}
		resolvedNodes[n.NodeID] = true
	}
	nodes := make([]previewNode, 0, len(rc.Nodes))
	// N-014：会签告警阈值取 spec（chain.json#roles.supervisor.multi_candidate_policy.warn_threshold；
	// 缺省 3 —— 与裁定一致，spec 结构化字段为权威）。
	warnThreshold := 3
	if mc := d.Spec.Chain.Roles["supervisor"].MultiCandidatePolicy; mc != nil && mc.WarnThreshold > 0 {
		warnThreshold = mc.WarnThreshold
	}
	warnings := []string{}
	for _, n := range rc.Nodes {
		pn := previewNode{
			Seq: n.Seq, SourceNodeID: n.SourceNodeID, NodeName: n.NodeName,
			ActorRole: n.ActorRole, IsApproval: n.IsApproval, BranchNote: n.BranchNote,
			Approvers: approversByNode[n.SourceNodeID],
			Resolved:  !n.IsApproval || resolvedNodes[n.SourceNodeID],
		}
		if pn.Approvers == nil {
			pn.Approvers = []previewApprover{}
		}
		if pn.IsApproval {
			pn.CoSignCount = len(pn.Approvers)
			// N-014 硬约束①：≥2 必须显式标注「本节点 N 人会签」（申请人要等几个人）。
			if pn.CoSignCount >= 2 {
				pn.BranchNote = joinNotes(pn.BranchNote, fmt.Sprintf("本节点 %d 人会签", pn.CoSignCount))
			}
			// N-014 硬约束②：≥warn_threshold 必须告警（**不阻断** —— 可能是配置错，但不拦业务）。
			if pn.CoSignCount >= warnThreshold {
				warnings = append(warnings, fmt.Sprintf(
					"节点「%s」为 %d 人会签（≥%d），请核实角色配置是否正确", pn.NodeName, pn.CoSignCount, warnThreshold))
			}
		}
		nodes = append(nodes, pn)
	}
	unresolved := rc.Unresolved
	if unresolved == nil {
		unresolved = []chain.UnresolvedRole{}
	}
	return ok(c, previewResponse{
		SpecVersion: rc.SpecVersion,
		Tier:        rc.Route.Tier,
		Route: previewRoute{
			ID: rc.Route.RouteID, Label: routeDef.Label,
			EnvCount: routeDef.EnvCount, Payment: routeDef.Payment,
		},
		Nodes:           nodes,
		UnresolvedRoles: unresolved,
		Warnings:        warnings,
		PaymentRoute:    paymentRoute,
	})
}

// joinNotes 拼接分支说明（避免覆盖既有 BranchNote）。
func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}
