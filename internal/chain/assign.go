package chain

import (
	"context"
	"fmt"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
)

// RoleQuery 角色候选查询（chain → RoleSource 端口的入参）。
// Role 为 t_user_role.role 的**中文角色名**（与 seed.Roles 同源）；
// MatchDept=true 时附加「主部门相等 或 分管部门(extra_depts)包含 Department」过滤。
type RoleQuery struct {
	Role       string
	Department string
	MatchDept  bool
}

// RoleCandidate 候选审批人。
type RoleCandidate struct {
	OpenID string
	Name   string
}

// RoleSource 候选人查询端口（消费方定义，实现＝internal/store；与 flow.Sender 同方向纪律）。
type RoleSource interface {
	Candidates(ctx context.Context, q RoleQuery) ([]RoleCandidate, error)
}

// UnresolvedRole 算不到人的节点（FR-M9-02：提交时阻断，preview 时可见）。
// ★ json tag（N-068 ①）：输出键名对齐 docs/05 §3.13 契约 {node_id,node_name,role,reason}
// （全仓同类结构均 snake_case —— 此处原为唯一漏网的大写驼峰，会让前端/脚本取值落空）。
type UnresolvedRole struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Role     string `json:"role"`
	Reason   string `json:"reason"`
}

// roleLabelOverride chain roles.label 与 t_user_role.role（seed.Roles）的个别差异。
// ★ inspector_group 的 chain label 是「验收人（按品类三组）」，权限表登记名是「验收人」。
var roleLabelOverride = map[string]string{
	"inspector_group": "验收人",
}

// queryFor 把链上的角色键解析为「查什么角色」——含主管领导的部门归属规则
// （chain.json roles 的机器可读数组，spec 有据）：
//   - 部门 ∈ project_general_manager.is_also_supervisor_for ⇒ 查「项目总经理」（全局）
//   - 部门 ∈ deputy_general_manager.is_supervisor_for       ⇒ 查「副总」（全局）
//   - 其余 ⇒ 查「主管领导」且 department/extra_depts 命中申请部门（回落，R-e 建议结构化）
func (s *Service) queryFor(roleKey string, f Facts) (RoleQuery, error) {
	if roleKey == "supervisor" {
		pgm := s.B.Chain.Roles["project_general_manager"]
		if containsDept(pgm.IsAlsoSupervisorFor, f.Department) {
			return RoleQuery{Role: pgm.Label}, nil
		}
		deputy := s.B.Chain.Roles["deputy_general_manager"]
		if containsDept(deputy.IsSupervisorFor, f.Department) {
			return RoleQuery{Role: deputy.Label}, nil
		}
		return RoleQuery{Role: s.B.Chain.Roles["supervisor"].Label, Department: f.Department, MatchDept: true}, nil
	}
	role, ok := s.B.Chain.Roles[roleKey]
	if !ok {
		return RoleQuery{}, fmt.Errorf("chain: chain.json roles 缺角色键 %q", roleKey)
	}
	label := role.Label
	if ov, ok := roleLabelOverride[roleKey]; ok {
		label = ov
	}
	return RoleQuery{Role: label}, nil
}

// Resolve 把角色级链解析为 flow.NodeSpec（审批任务）。
// 返回值同时携带未解析角色（不吞错）——preview 直接展示；提交侧用 EnsureResolvable 阻断。
//
// 解析纪律：
//   - 候选 = t_user_role.active=1 且镜像在用（镜像**仅做过滤**，不作授权源 —— 「镜像≠权限」）；
//   - 0 候选 ⇒ UnresolvedRole（提交阻断）；
//   - ≥2 候选 ⇒ 全员作为该节点会签审批人（R-c 过渡口径，议题待裁定）。
func (s *Service) Resolve(ctx context.Context, nodes []RoleNode, f Facts) ([]flow.NodeSpec, []UnresolvedRole, error) {
	specs := []flow.NodeSpec{}
	unresolved := []UnresolvedRole{}
	for _, n := range nodes {
		if !n.IsApproval {
			continue
		}
		// ★ N-065 T2：generates_task 显式的 applicant 待办 —— 办理人＝申请人本人
		//（动作型 actor 无角色候选可查；身份由提交/预览侧随 Facts 传入）。
		// 缺省空 ⇒ 记 unresolved 可见失败（绝不静默生成无人可办的任务）。
		if n.ActorRole == "applicant" {
			if strings.TrimSpace(f.ApplicantOpenID) == "" {
				unresolved = append(unresolved, UnresolvedRole{
					NodeID: n.SourceNodeID, NodeName: n.NodeName, Role: "applicant",
					Reason: "申请人身份未随 Facts 传入（generates_task 显式待办的办理人＝申请人本人）",
				})
				continue
			}
			specs = append(specs, flow.NodeSpec{
				NodeID: n.SourceNodeID, NodeName: n.NodeName, Seq: n.Seq,
				Approvers: []flow.Approver{{OpenID: f.ApplicantOpenID, Name: f.ApplicantName}},
			})
			continue
		}
		q, err := s.queryFor(n.ActorRole, f)
		if err != nil {
			return nil, nil, err
		}
		cands, err := s.Roles.Candidates(ctx, q)
		if err != nil {
			return nil, nil, fmt.Errorf("chain: 解析角色 %s（节点 %s）失败: %w", q.Role, n.SourceNodeID, err)
		}
		if len(cands) == 0 {
			reason := "无在岗候选"
			if q.MatchDept {
				reason = fmt.Sprintf("无在岗主管领导分管 %s", q.Department)
			}
			unresolved = append(unresolved, UnresolvedRole{
				NodeID: n.SourceNodeID, NodeName: n.NodeName, Role: q.Role, Reason: reason,
			})
			continue
		}
		approvers := make([]flow.Approver, 0, len(cands))
		for _, c := range cands {
			approvers = append(approvers, flow.Approver{OpenID: c.OpenID, Name: c.Name})
		}
		specs = append(specs, flow.NodeSpec{
			NodeID: n.SourceNodeID, NodeName: n.NodeName, Seq: n.Seq, Approvers: approvers,
		})
	}
	return specs, unresolved, nil
}

func containsDept(list []string, dept string) bool {
	for _, d := range list {
		if strings.TrimSpace(d) == dept {
			return true
		}
	}
	return false
}
