package chain

// N-057 ② · 节点条件分支数据驱动（branchByID 绑定 ＋ actor 唯一来源 ＋ fail-visible）。
// 装配照 chain_test#loadBundle（与生产同一份 embed 字节；每用例各自 loadBundle ⇒ 变异互不污染）。
// ★ 变异注入：Routes 是 map[string]RouteDoc（值）⇒ 取-改-写回（map 值不可寻址）；
//   Nodes/Branches 切片共享底层数组 ⇒ 索引改动能穿透到 BuildNodes 读取的那份数据。

import (
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// pet1Branch0 取 purchase_tier1.nodes[approve_petty_cash].branches[0] 的可写视图
// （取-改-放回三步）。
func pet1Branch0(t *testing.T, b *specload.Bundle, mutate func(*specload.NodeBranch)) {
	t.Helper()
	rd := b.Chain.Routes["purchase_tier1"]
	var target *specload.NodeBranch
	for i := range rd.Nodes {
		if rd.Nodes[i].ID == "approve_petty_cash" && len(rd.Nodes[i].Branches) > 0 {
			target = &rd.Nodes[i].Branches[0]
			break
		}
	}
	if target == nil {
		t.Fatal("spec 结构变了：purchase_tier1 无 approve_petty_cash.branches[0]")
	}
	mutate(target)
	b.Chain.Routes["purchase_tier1"] = rd // 写回（Nodes/Branches 切片共享，改动已穿透）
}

// pet1Node 跑 BuildNodes 并取 approve_petty_cash 节点。
func pet1Node(t *testing.T, b *specload.Bundle, f Facts) (RoleNode, error) {
	t.Helper()
	nodes, err := BuildNodes(b, "purchase_tier1", f)
	if err != nil {
		return RoleNode{}, err
	}
	for _, n := range nodes {
		if n.SourceNodeID == "approve_petty_cash" {
			return n, nil
		}
	}
	t.Fatal("approve_petty_cash 节点缺失")
	return RoleNode{}, nil
}

var factsTrue = Facts{DocType: DocBA, AmountCents: i64(99999), ApplicantIsOpsSupervisor: true}
var factsFalse = Facts{DocType: DocBA, AmountCents: i64(99999), ApplicantIsOpsSupervisor: false}

// D1 数据驱动（★ 本批主证）：spec actor 改 deputy_general_manager ⇒ 输出跟着变。
func TestNodeBranchD1ActorFromSpec(t *testing.T) {
	b := loadBundle(t)
	pet1Branch0(t, b, func(br *specload.NodeBranch) { br.Actor = "deputy_general_manager" })
	n, err := pet1Node(t, b, factsTrue)
	if err != nil {
		t.Fatalf("D1: %v", err)
	}
	if n.ActorRole != "deputy_general_manager" {
		t.Errorf("D1: ActorRole=%q，期望 deputy_general_manager（spec 驱动 —— 写死 supervisor 的实现必红）", n.ActorRole)
	}
}

// D2 绑定键是 id（不是「第一条分支」）：id 改 other_fact ⇒ 分支不存在。
// ★ 与任务包 D2 期望栏的字面冲突处置：任务包 D2 写「上抬不生效 ⇒ ops_supervisor」，
// 但 T2#4 fail-visible 明写「事实为真、但缺 id==applicant_is_ops_supervisor 的分支
// ⇒ **可见失败**」—— D2 构造（id 改名＋事实为真）正是 T2#4 场景。两者不可同时成立；
// 按 **fail-visible 主规格（T2#4）** 断言可见错误。★ M3 鉴别力不受影响：
// 「取 branches[0] 不看 id」的实现会让位置上的分支命中 ⇒ 上抬成功 ⇒ 本断言破。
func TestNodeBranchD2BindByID(t *testing.T) {
	b := loadBundle(t)
	pet1Branch0(t, b, func(br *specload.NodeBranch) { br.ID = "other_fact" })
	_, err := pet1Node(t, b, factsTrue)
	if err == nil {
		t.Fatal("D2: id 改名后应按 T2#4 可见失败（按位置取 branches[0] 的实现会命中并上抬 —— 本断言即 M3 鉴别力）")
	}
	if !strings.Contains(err.Error(), "缺少分支 applicant_is_ops_supervisor") {
		t.Errorf("D2: 错误应点名缺失的分支 id（绑定键按 id、不按位置）：%v", err)
	}
}

// D3 fail-visible：事实为真但 actor 为空 ⇒ 可见错误含分支 id。
func TestNodeBranchD3MissingActorVisible(t *testing.T) {
	b := loadBundle(t)
	pet1Branch0(t, b, func(br *specload.NodeBranch) { br.Actor = "" })
	_, err := pet1Node(t, b, factsTrue)
	if err == nil {
		t.Fatal("D3: actor 为空应可见失败（静默保留默认角色＝假绿）")
	}
	if !strings.Contains(err.Error(), "applicant_is_ops_supervisor") {
		t.Errorf("D3: 错误应含分支 id applicant_is_ops_supervisor：%v", err)
	}
}

// D4 fail-visible：actor 非审批角色（purchaser=action actor）⇒ 可见错误含实际 actor 值。
func TestNodeBranchD4NonApproverActorVisible(t *testing.T) {
	b := loadBundle(t)
	pet1Branch0(t, b, func(br *specload.NodeBranch) { br.Actor = "purchaser" })
	_, err := pet1Node(t, b, factsTrue)
	if err == nil {
		t.Fatal("D4: 非审批角色 actor 应可见失败")
	}
	if !strings.Contains(err.Error(), "purchaser") {
		t.Errorf("D4: 错误应含实际 actor 值 purchaser：%v", err)
	}
}

// D5 回归：事实为假 ⇒ 不上抬（role=ops_supervisor、note 空）。
func TestNodeBranchD5FactFalseNoLift(t *testing.T) {
	b := loadBundle(t)
	n, err := pet1Node(t, b, factsFalse)
	if err != nil {
		t.Fatalf("D5: %v", err)
	}
	if n.ActorRole != "ops_supervisor" || n.BranchNote != "" {
		t.Errorf("D5: 事实为假应 role=ops_supervisor note=空，实为 role=%q note=%q", n.ActorRole, n.BranchNote)
	}
}

// D6 回归：真 spec ＋ 事实为真 ⇒ 上抬仍生效（role=supervisor、note 非空）。
// ★ 与既有 TestBuildNodes/BA_R03上抬 同判据的冗余保留（任务包明示不得删既有那条）。
func TestNodeBranchD6RealSpecLift(t *testing.T) {
	b := loadBundle(t)
	n, err := pet1Node(t, b, factsTrue)
	if err != nil {
		t.Fatalf("D6: %v", err)
	}
	if n.ActorRole != "supervisor" || n.BranchNote == "" {
		t.Errorf("D6: 真 spec 上抬应 role=supervisor note 非空，实为 role=%q note=%q", n.ActorRole, n.BranchNote)
	}
}
