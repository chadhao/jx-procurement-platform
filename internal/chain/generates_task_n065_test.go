package chain

// N-065 T2：`generates_task` 显式优先 ＋ applicant 待办办理人解析（可达性的「人的待办」半边）。

import (
	"context"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func nodeIdxByID(nodes []specload.NodeDoc, id string) int {
	for i := range nodes {
		if nodes[i].ID == id {
			return i
		}
	}
	return -1
}

// TestGeneratesTaskExplicitPriorityN065 三态：显式 true 生成 / 键缺失回缺省 / 显式 false 抑制。
// ★ 零回归证据＝②：把 spec 声明从**内存副本**摘掉 ⇒ 任务集回到改前现状
// （证明该键是唯一行为来源，不是顺带改出来的）。
func TestGeneratesTaskExplicitPriorityN065(t *testing.T) {
	b := loadBundle(t)
	f := Facts{DocType: DocBA, AmountCents: i64(99999)}

	// ① spec 现状（return_receipt: generates_task=true）⇒ 生成审批任务。
	nodes, err := BuildNodes(b, "purchase_tier1", f)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(approvalIDs(nodes), "return_receipt") {
		t.Fatalf("显式 true 应生成 return_receipt 任务, 实为 %v", approvalIDs(nodes))
	}

	// ② 零回归：内存摘掉该键（键缺失 ⇒ isActionActor 缺省 ⇒ applicant 不生成）。
	rd := b.Chain.Routes["purchase_tier1"]
	if nodeIdxByID(rd.Nodes, "return_receipt") < 0 {
		t.Fatal("spec 缺 return_receipt 节点")
	}
	rd.Nodes[nodeIdxByID(rd.Nodes, "return_receipt")].GeneratesTask = nil
	nodes2, err := BuildNodes(b, "purchase_tier1", f)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"approve_petty_cash", "disburse"}
	if strings.Join(approvalIDs(nodes2), ",") != strings.Join(want, ",") {
		t.Errorf("摘掉声明后审批节点 = %v，应回到现状 %v", approvalIDs(nodes2), want)
	}

	// ③ 显式 false 优先于审批角色（反向：disburse 是 ops_supervisor 审批节点，
	//    显式 false ⇒ 不生成 —— 证明显式声明对两个方向都压过缺省）。
	falseVal := false
	rd.Nodes[nodeIdxByID(rd.Nodes, "disburse")].GeneratesTask = &falseVal
	nodes3, err := BuildNodes(b, "purchase_tier1", f)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(approvalIDs(nodes3), "disburse") {
		t.Errorf("显式 false 应抑制 disburse 任务, 实为 %v", approvalIDs(nodes3))
	}
	if !containsID(approvalIDs(nodes3), "approve_petty_cash") {
		t.Errorf("显式 false 不应误伤其它节点: %v", approvalIDs(nodes3))
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestResolveApplicantAssigneeN065 applicant 待办办理人＝Facts 传入的申请人本人；
// 身份缺失 ⇒ unresolved 可见失败（不静默生成无人可办的任务）。
func TestResolveApplicantAssigneeN065(t *testing.T) {
	s := &Service{B: loadBundle(t)} // applicant 分支不触角色源 ⇒ Roles 可为 nil
	nodes := []RoleNode{{
		SourceNodeID: "return_receipt", NodeName: "回交凭据",
		ActorRole: "applicant", IsApproval: true, Seq: 3,
	}}

	spec, unres, err := s.Resolve(context.Background(), nodes,
		Facts{ApplicantOpenID: "ou_app", ApplicantName: "张三"})
	if err != nil {
		t.Fatal(err)
	}
	if len(unres) != 0 || len(spec) != 1 {
		t.Fatalf("解析结果 spec=%d unres=%d, 期望 1/0", len(spec), len(unres))
	}
	if spec[0].Approvers[0].OpenID != "ou_app" || spec[0].Approvers[0].Name != "张三" {
		t.Errorf("办理人 = %+v, 期望 ou_app/张三", spec[0].Approvers[0])
	}
	if spec[0].Seq != 3 {
		t.Errorf("Seq = %d, 期望 3", spec[0].Seq)
	}

	_, unres2, err := s.Resolve(context.Background(), nodes, Facts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(unres2) != 1 || !strings.Contains(unres2[0].Reason, "申请人身份未随 Facts 传入") {
		t.Errorf("身份缺失应记 unresolved 可见失败, 实为 %+v", unres2)
	}
}
