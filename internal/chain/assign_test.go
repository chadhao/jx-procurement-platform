package chain

// M2 后半验收：角色→人解析（端口伪造，不碰 DB）。

import (
	"context"
	"errors"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// fakeRoleSource 按 Role 名回放候选，并记录收到的查询。
type fakeRoleSource struct {
	cands   map[string][]RoleCandidate
	queries []RoleQuery
	err     error
}

func (f *fakeRoleSource) Candidates(_ context.Context, q RoleQuery) ([]RoleCandidate, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.cands[q.Role], nil
}

func newService(src RoleSource) *Service {
	b, err := specload.Load(specfs.FS)
	if err != nil {
		panic(err)
	}
	return &Service{B: b, Roles: src}
}

func TestQueryForSupervisorDepartments(t *testing.T) {
	src := &fakeRoleSource{cands: map[string][]RoleCandidate{}}
	s := newService(src)

	cases := []struct {
		dept string
		want RoleQuery
	}{
		{"综合运营部", RoleQuery{Role: "项目总经理"}},
		{"质检技术部", RoleQuery{Role: "项目总经理"}},
		{"生产部", RoleQuery{Role: "副总"}},
		{"销售部", RoleQuery{Role: "副总"}},
		{"仓储部", RoleQuery{Role: "主管领导", Department: "仓储部", MatchDept: true}},
	}
	for _, c := range cases {
		got, err := s.queryFor("supervisor", Facts{Department: c.dept})
		if err != nil {
			t.Fatalf("dept=%s: %v", c.dept, err)
		}
		if got != c.want {
			t.Errorf("dept=%s: queryFor = %+v，应为 %+v", c.dept, got, c.want)
		}
	}
}

func TestQueryForLabelOverride(t *testing.T) {
	s := newService(&fakeRoleSource{})
	got, err := s.queryFor("inspector_group", Facts{})
	if err != nil {
		t.Fatal(err)
	}
	// chain label 是「验收人（按品类三组）」，权限表登记名是「验收人」
	if got.Role != "验收人" {
		t.Errorf("inspector_group 映射 = %q，应为「验收人」", got.Role)
	}
}

func TestResolveUnresolvedAndResolvable(t *testing.T) {
	amt := int64(100000)

	t.Run("缺人_阻断", func(t *testing.T) {
		src := &fakeRoleSource{cands: map[string][]RoleCandidate{
			"综合运营主管": {{OpenID: "ou_ops", Name: "运营"}},
			"项目总经理":  {{OpenID: "ou_pgm", Name: "总"}},
			// 主管领导（fallback）空缺
		}}
		s := newService(src)
		rc, err := s.Compute(context.Background(), Facts{
			DocType: DocPR, AmountCents: &amt, Department: "仓储部",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rc.Unresolved) == 0 {
			t.Fatal("期望 Unresolved 非空（主管领导空缺）")
		}
		if err := rc.EnsureResolvable(); !errors.Is(err, ErrUnresolvable) {
			t.Errorf("EnsureResolvable = %v，应为 ErrUnresolvable", err)
		}
	})

	t.Run("多人_会签放行", func(t *testing.T) {
		src := &fakeRoleSource{cands: map[string][]RoleCandidate{
			"综合运营主管": {{OpenID: "ou_ops", Name: "运营"}},
			"项目总经理":  {{OpenID: "ou_pgm1", Name: "总1"}, {OpenID: "ou_pgm2", Name: "总2"}},
			"主管领导":   {{OpenID: "ou_sup", Name: "主管"}},
		}}
		s := newService(src)
		rc, err := s.Compute(context.Background(), Facts{
			DocType: DocPR, AmountCents: &amt, Department: "仓储部",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := rc.EnsureResolvable(); err != nil {
			t.Fatalf("不应阻断: %v", err)
		}
		// 合同二级节点应有 2 名会签审批人（R-c 过渡口径）
		for _, n := range rc.Spec {
			if n.NodeID == "contract_pgm" && len(n.Approvers) != 2 {
				t.Errorf("contract_pgm 审批人数 = %d，应为 2（会签）", len(n.Approvers))
			}
		}
		// 审批任务序连续
		for i, n := range rc.Spec {
			if n.Seq != i+1 {
				t.Errorf("Spec[%d].Seq = %d，应为 %d", i, n.Seq, i+1)
			}
		}
	})

	t.Run("查询错误_上抛", func(t *testing.T) {
		s := newService(&fakeRoleSource{err: errors.New("db down")})
		_, err := s.Compute(context.Background(), Facts{DocType: DocPR, AmountCents: &amt, Department: "生产部"})
		if err == nil {
			t.Fatal("期望错误上抛")
		}
	})
}
