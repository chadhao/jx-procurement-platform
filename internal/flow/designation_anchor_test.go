package flow_test

// N-035 锚定：`PR#no_self_purchaser_at_designation` 的规则来源是 spec ——
// 本测试直读与生产同一份 embed 字节（N-008），spec 侧改动（删判据 / 改 when /
// 改 carried_by）使锚定转红，逼实现与规格同步，防「两份真相」。
//
// ★ 为什么用锚定而非让 flow 直读 bundle：`flow.Service` 现无 spec 注入，
//   改签名会扩散 55+ 调用点与装配层；`carried_by` 已由 WorkBuddy 指向本文件
//   （designation.go）＝双向锚：spec 指向代码、本测试把 spec 结构钉住。
//   这正是 N-035 建议方案给的替代路径（「若读 spec 成本高，以 carried_by 引用定位」）。

import (
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func TestDesignationSpecAnchor(t *testing.T) {
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatalf("spec 加载失败: %v", err)
	}
	form := b.Forms["PR"]
	if form.DocType == "" {
		t.Fatal("spec 缺 PR 表单")
	}
	var found *specload.CheckDoc
	for i := range form.Checks {
		if form.Checks[i].ID == "no_self_purchaser_at_designation" {
			found = &form.Checks[i]
		}
	}
	if found == nil {
		t.Fatal("PR 表单缺判据 no_self_purchaser_at_designation —— N-035 拆出的审批时点判据不见了；" +
			"若已改名/删除，designation.go 的自批自派拦截须同步（锚定测试即为此而设）")
	}
	if found.Severity != "hard" {
		t.Errorf("severity = %q, 期望 hard（声称的拦截必须是硬的）", found.Severity)
	}
	if found.When != "approval(supervisor_approval)" {
		t.Errorf("when = %q, 期望 approval(supervisor_approval)（一条判据只声称一个时点 —— conventions.checks_when）", found.When)
	}
	if !strings.Contains(found.CarriedBy, "internal/flow/designation.go") {
		t.Errorf("carried_by = %q, 期望指向 internal/flow/designation.go（承载者定位断了 = 执行体可能已漂移）", found.CarriedBy)
	}
	// 附带钉住 N-035 的另一半：提交时点判据仍在（防拆分后丢失）
	hasSubmit := false
	for _, c := range form.Checks {
		if c.ID == "no_self_purchaser" && c.When == "submit" {
			hasSubmit = true
		}
	}
	if !hasSubmit {
		t.Error("PR#no_self_purchaser（when=submit）不见了 —— 提交防伪造那一半被误删")
	}
}
