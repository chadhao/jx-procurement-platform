package flow

// N-038 项② 内部测试（applyBizFields 未导出 ⇒ 同包测试）：
// 客户端伪造的 applicant / applicant_department 不得残留 ext_json。

import (
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

func TestApplyBizFieldsDropsIdentityForgery(t *testing.T) {
	inst := &store.Instance{BizNo: "PR-2609-9001"}
	ext, err := applyBizFields(inst, map[string]any{
		"applicant":            "ou_攻击者",
		"applicant_department": "伪造部门",
		"remark":               "合法透传键",
		"usage_category_l1":    "P01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ext, "ou_攻击者") {
		t.Errorf("伪造 applicant 残留 ext: %s", ext)
	}
	if strings.Contains(ext, "伪造部门") {
		t.Errorf("伪造 applicant_department 残留 ext: %s", ext)
	}
	if !strings.Contains(ext, "usage_category_l1") {
		t.Errorf("合法业务字段被误杀: %s", ext)
	}
	if !strings.Contains(ext, "remark") {
		t.Errorf("透传键被误杀: %s", ext)
	}
}
