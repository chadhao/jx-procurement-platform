package chain

// 批 1 · M2 前半验收：分档边界 / 路线判定 / 链展开。
// ★ 测试输入＝与生产同一份 embed 字节（specfs.FS，N-008 修正②），不复制快照副本。

import (
	"errors"
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func loadBundle(t *testing.T) *specload.Bundle {
	t.Helper()
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatalf("spec 加载失败: %v", err)
	}
	return b
}

func i64(v int64) *int64 { return &v }

func TestTierOfBoundaries(t *testing.T) {
	b := loadBundle(t)
	// chain.json conventions.boundary_semantics.verified 四条（999/1000/5000/5001 元）
	cases := []struct {
		cents int64
		want  string
	}{
		{99999, "purchase_tier1"},
		{100000, "purchase_tier2"},
		{500000, "purchase_tier2"},
		{500001, "purchase_tier3"},
		{20000000, "purchase_tier3"},
	}
	for _, c := range cases {
		got, err := TierOf(b, c.cents)
		if err != nil {
			t.Errorf("TierOf(%d) 报错: %v", c.cents, err)
			continue
		}
		if got != c.want {
			t.Errorf("TierOf(%d) = %s，应为 %s", c.cents, got, c.want)
		}
	}
}

func TestResolveRoute(t *testing.T) {
	b := loadBundle(t)
	cases := []struct {
		name    string
		f       Facts
		want    string
		wantErr error
	}{
		{"BA_采一", Facts{DocType: DocBA, AmountCents: i64(99999)}, "purchase_tier1", nil},
		{"BA_超一档拒", Facts{DocType: DocBA, AmountCents: i64(100000)}, "", ErrTierOutOfDoc},
		{"BA_缺金额", Facts{DocType: DocBA}, "", ErrAmountMissing},
		{"PR_采一拒导BA", Facts{DocType: DocPR, AmountCents: i64(99999)}, "", ErrTierOutOfDoc},
		{"PR_采二", Facts{DocType: DocPR, AmountCents: i64(100000)}, "purchase_tier2", nil},
		{"PR_采二上界", Facts{DocType: DocPR, AmountCents: i64(500000)}, "purchase_tier2", nil},
		{"PR_采三", Facts{DocType: DocPR, AmountCents: i64(500001)}, "purchase_tier3", nil},
		{"SA_销类", Facts{DocType: DocSA, UsageCategoryL1: "S03"}, "expense_sales", nil},
		{"SA_M04销线", Facts{DocType: DocSA, UsageCategoryL1: "M04"}, "expense_sales", nil},
		{"SA_M01直付", Facts{DocType: DocSA, UsageCategoryL1: "M01", PaymentMethodInput: "corporate_direct"}, "expense_mgmt_direct", nil},
		{"SA_M01垫付", Facts{DocType: DocSA, UsageCategoryL1: "M01", PaymentMethodInput: "personal_advance"}, "expense_mgmt_advance", nil},
		{"SA_M01垫付中文输入", Facts{DocType: DocSA, UsageCategoryL1: "M01", PaymentMethodInput: "个人垫付"}, "expense_mgmt_advance", nil},
		{"SA_M01直付中文输入", Facts{DocType: DocSA, UsageCategoryL1: "M02", PaymentMethodInput: "对公直付"}, "expense_mgmt_direct", nil},
		{"SA_M07恒直付", Facts{DocType: DocSA, UsageCategoryL1: "M07", PaymentMethodInput: "personal_advance"}, "expense_mgmt_direct", nil},
		{"SA_P08恒直付", Facts{DocType: DocSA, UsageCategoryL1: "P08"}, "expense_mgmt_direct", nil},
		{"SA_P01拒", Facts{DocType: DocSA, UsageCategoryL1: "P01"}, "", ErrCategoryInvalid},
		{"SA_M01支付方式缺", Facts{DocType: DocSA, UsageCategoryL1: "M01"}, "", ErrPaymentInvalid},
		{"SA_未知分类", Facts{DocType: DocSA, UsageCategoryL1: "Z99"}, "", ErrCategoryInvalid},
		{"批1之外单据", Facts{DocType: "CT", AmountCents: i64(1000)}, "", ErrUnsupportedDoc},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveRoute(b, c.f)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v，应为 %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误: %v", err)
			}
			if got.RouteID != c.want {
				t.Errorf("route = %s，应为 %s", got.RouteID, c.want)
			}
		})
	}
}

func approvalIDs(nodes []RoleNode) []string {
	out := []string{}
	for _, n := range nodes {
		if n.IsApproval {
			out = append(out, n.SourceNodeID)
		}
	}
	return out
}

func TestBuildNodes(t *testing.T) {
	b := loadBundle(t)

	t.Run("BA_常规_审批为备付金上抬前", func(t *testing.T) {
		nodes, err := BuildNodes(b, "purchase_tier1", Facts{DocType: DocBA, AmountCents: i64(99999)})
		if err != nil {
			t.Fatal(err)
		}
		got := approvalIDs(nodes)
		want := []string{"approve_petty_cash", "disburse"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("审批节点 = %v，应为 %v", got, want)
		}
		// 非审批环节（record / anti_split / self_purchase / return_receipt）仍在链上
		if len(nodes) != 6 {
			t.Errorf("总环节数 = %d，应为 6（env_count）", len(nodes))
		}
	})

	t.Run("BA_R03上抬", func(t *testing.T) {
		nodes, err := BuildNodes(b, "purchase_tier1",
			Facts{DocType: DocBA, AmountCents: i64(99999), ApplicantIsOpsSupervisor: true})
		if err != nil {
			t.Fatal(err)
		}
		var pc *RoleNode
		for i := range nodes {
			if nodes[i].SourceNodeID == "approve_petty_cash" {
				pc = &nodes[i]
			}
		}
		if pc == nil {
			t.Fatal("approve_petty_cash 节点缺失")
		}
		if pc.ActorRole != "supervisor" || pc.BranchNote == "" {
			t.Errorf("R-03 上抬未命中：role=%q note=%q", pc.ActorRole, pc.BranchNote)
		}
	})

	t.Run("PR_采二_合同两级展开", func(t *testing.T) {
		nodes, err := BuildNodes(b, "purchase_tier2", Facts{DocType: DocPR, AmountCents: i64(100000), Department: "生产部"})
		if err != nil {
			t.Fatal(err)
		}
		got := approvalIDs(nodes)
		want := []string{"supervisor_approval", "contract_supervisor", "contract_pgm", "ops_review", "submit_group"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("审批节点 = %v，应为 %v", got, want)
		}
		if len(got) != 5 {
			t.Errorf("采二审批任务数 = %d，应为 5（env_count）", len(got))
		}
	})

	t.Run("PR_采二_主管即总经理跳过合同二级", func(t *testing.T) {
		nodes, err := BuildNodes(b, "purchase_tier2",
			Facts{DocType: DocPR, AmountCents: i64(100000), Department: "综合运营部"})
		if err != nil {
			t.Fatal(err)
		}
		got := approvalIDs(nodes)
		want := []string{"supervisor_approval", "contract_supervisor", "ops_review", "submit_group"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("审批节点 = %v，应为 %v", got, want)
		}
		// 被跳过的二级以非审批环节留痕
		found := false
		for _, n := range nodes {
			if n.SourceNodeID == "contract_pgm" && !n.IsApproval && n.BranchNote != "" {
				found = true
			}
		}
		if !found {
			t.Error("contract_pgm 跳过留痕缺失")
		}
	})

	t.Run("PR_采三_七节点", func(t *testing.T) {
		nodes, err := BuildNodes(b, "purchase_tier3", Facts{DocType: DocPR, AmountCents: i64(500001), Department: "生产部"})
		if err != nil {
			t.Fatal(err)
		}
		got := approvalIDs(nodes)
		want := []string{"supervisor_approval", "supervisor_review", "pgm_confirm",
			"contract_supervisor", "contract_pgm", "ops_review", "submit_group"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("审批节点 = %v，应为 %v", got, want)
		}
		if len(got) != 7 {
			t.Errorf("采三审批任务数 = %d，应为 7（env_count）", len(got))
		}
	})

	t.Run("PR_采三_加强插入", func(t *testing.T) {
		nodes, err := BuildNodes(b, "purchase_tier3",
			Facts{DocType: DocPR, AmountCents: i64(20000001), Department: "生产部"})
		if err != nil {
			t.Fatal(err)
		}
		got := approvalIDs(nodes)
		want := []string{"supervisor_approval", "procure_method_confirm", "supervisor_review", "pgm_confirm",
			"contract_supervisor", "contract_pgm", "ops_review", "submit_group"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("审批节点 = %v，应为 %v", got, want)
		}
	})

	t.Run("SA_销线", func(t *testing.T) {
		nodes, err := BuildNodes(b, "expense_sales", Facts{DocType: DocSA, UsageCategoryL1: "S03"})
		if err != nil {
			t.Fatal(err)
		}
		got := approvalIDs(nodes)
		want := []string{"supervisor_approval", "ops_review", "submit_group"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("审批节点 = %v，应为 %v", got, want)
		}
		if len(nodes) != 5 {
			t.Errorf("总环节数 = %d，应为 5（env_count）", len(nodes))
		}
	})

	t.Run("SA_管二直付", func(t *testing.T) {
		nodes, err := BuildNodes(b, "expense_mgmt_direct",
			Facts{DocType: DocSA, UsageCategoryL1: "M01", PaymentMethodInput: "corporate_direct"})
		if err != nil {
			t.Fatal(err)
		}
		got := approvalIDs(nodes)
		want := []string{"supervisor_approval", "ops_review", "submit_group"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("审批节点 = %v，应为 %v", got, want)
		}
		if len(nodes) != 4 {
			t.Errorf("总环节数 = %d，应为 4（env_count）", len(nodes))
		}
	})
}

// TestTier3PlusAnchor 规格漂移守卫：spec 中 tier3_plus 的 when 必须仍含
// 金额阈值字面量（与代码常量 tier3PlusAmountCents 对应）。spec 一改，本测试即红。
func TestTier3PlusAnchor(t *testing.T) {
	b := loadBundle(t)
	br := b.Chain.Routes["purchase_tier3"].Branches["tier3_plus"]
	if !strings.Contains(br.When, "20000000") {
		t.Errorf("tier3_plus.when 缺金额阈值 20000000（实际 %q）——请同步 tier3PlusAmountCents", br.When)
	}
	if br.InsertBefore != "rfq" {
		t.Errorf("tier3_plus.insert_before = %q，应为 rfq", br.InsertBefore)
	}
	if len(br.InsertedNodes) != 3 {
		t.Errorf("tier3_plus 应插 3 节点，实为 %d", len(br.InsertedNodes))
	}
}
