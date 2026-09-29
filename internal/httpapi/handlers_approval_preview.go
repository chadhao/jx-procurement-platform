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
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
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
	})
}
