package chain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// ErrUnresolvable 链上存在算不到人的节点 —— 提交阻断（FR-M9-02）。
// handler 映射为 40000 + error_detail.unresolved_roles（N-018 过渡口径）。
var ErrUnresolvable = errors.New("chain: 审批链存在无法解析的角色")

// Service 分档/链计算与解析（preview 与 submit 共用同一入口 —— D5）。
type Service struct {
	B     *specload.Bundle
	Roles RoleSource
}

// ResolvedChain 一次计算的完整产物。
type ResolvedChain struct {
	SpecVersion string          // N-008③：随 preview/响应带给前端核对契约版本
	Route       RouteResult     // 判定结果（tier + route）
	Nodes       []RoleNode      // 角色级全流程（含非审批环节，preview 展示用）
	Spec        []flow.NodeSpec // 审批任务（提交/预览共用）
	Unresolved  []UnresolvedRole
}

// Compute 由 Facts 计算分档、路线、链与审批人解析。
// 只在「输入非法」（档位不匹配/分类无法归线/角色键缺失/查询失败）时返回错误；
// 「算不到人」不在此报错，记入 Unresolved（preview 要展示，提交侧用 EnsureResolvable 阻断）。
func (s *Service) Compute(ctx context.Context, f Facts) (*ResolvedChain, error) {
	route, err := ResolveRoute(s.B, f)
	if err != nil {
		return nil, err
	}
	nodes, err := BuildNodes(s.B, route.RouteID, f)
	if err != nil {
		return nil, err
	}
	spec, unresolved, err := s.Resolve(ctx, nodes, f)
	if err != nil {
		return nil, err
	}
	return &ResolvedChain{
		SpecVersion: s.B.SpecVersion,
		Route:       route,
		Nodes:       nodes,
		Spec:        spec,
		Unresolved:  unresolved,
	}, nil
}

// EnsureResolvable 提交侧阻断：存在未解析角色即返回 ErrUnresolvable（带明细）。
func (rc *ResolvedChain) EnsureResolvable() error {
	if len(rc.Unresolved) == 0 {
		return nil
	}
	parts := make([]string, 0, len(rc.Unresolved))
	for _, u := range rc.Unresolved {
		parts = append(parts, fmt.Sprintf("%s（%s）：%s", u.NodeName, u.Role, u.Reason))
	}
	return fmt.Errorf("%w: %s", ErrUnresolvable, strings.Join(parts, "；"))
}
