package approval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/approval"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// defregistry_feishucode_test.go —— 双 code 池落库消歧（docs/16 G-8 / §2-C，0013 批）：
// 主键 approval_code ＝ 我方自定义 code（不变）；POST 响应回填值落 feishu_code。
// ★ R26 纪律：0013 新列 feishu_code 的真实写入者＝本路径（Registry.Register）。

// TestRegisterWritesFeishuCodeKeepsCustomPK 注册后：approval_code 保持我方自定义 code，
// feishu_code ＝ POST 响应回填值（归属未实测 V-4 ⇒ 双写不猜）。
func TestRegisterWritesFeishuCodeKeepsCustomPK(t *testing.T) {
	db := storetest.NewDB(t)
	fake := feishu.NewFakeExternalApprovalClient()
	fake.UpsertFn = func(ctx context.Context, def feishu.ExternalApprovalDef) (feishu.ExternalApprovalResult, error) {
		// 模拟平台回填一个与入参不同的 code（双池归属待 V-4 实测）。
		return feishu.ExternalApprovalResult{ApprovalCode: "code-response", FeishuLogID: "log-1"}, nil
	}
	reg := approval.NewRegistry(db, fake, nil)

	item, err := reg.Register(context.Background(), sampleDef())
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if item.ApprovalCode != "code-pr" {
		t.Errorf("SyncItem.ApprovalCode 应为我方自定义 code，实际 %q", item.ApprovalCode)
	}
	def, err := db.GetApprovalDef(context.Background(), "code-pr")
	if err != nil {
		t.Fatalf("按自定义 code 查定义失败: %v", err)
	}
	if def.ApprovalCode != "code-pr" {
		t.Errorf("主键 approval_code 应保持我方自定义 code，实际 %q", def.ApprovalCode)
	}
	if def.FeishuCode != "code-response" {
		t.Errorf("feishu_code 应落 POST 响应回填值，实际 %q", def.FeishuCode)
	}
}

// TestRegisterFeishuCodeFallbackWhenResponseEmpty 平台响应未回填 code ⇒
// feishu_code 回退为入参 code（与 UpsertExternalApproval 的 firstNonEmpty 口径一致），
// 主键不受影响。
func TestRegisterFeishuCodeFallbackWhenResponseEmpty(t *testing.T) {
	db := storetest.NewDB(t)
	fake := feishu.NewFakeExternalApprovalClient()
	fake.UpsertFn = func(ctx context.Context, def feishu.ExternalApprovalDef) (feishu.ExternalApprovalResult, error) {
		return feishu.ExternalApprovalResult{}, errors.New("不应走到此处") // 被 UpsertFn 覆盖前先确认签名
	}
	// 覆盖为不回填 code 的正常返回。
	fake.UpsertFn = func(ctx context.Context, def feishu.ExternalApprovalDef) (feishu.ExternalApprovalResult, error) {
		return feishu.ExternalApprovalResult{ApprovalCode: def.ApprovalCode}, nil
	}
	reg := approval.NewRegistry(db, fake, nil)

	if _, err := reg.Register(context.Background(), sampleDef()); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	def, err := db.GetApprovalDef(context.Background(), "code-pr")
	if err != nil {
		t.Fatal(err)
	}
	if def.FeishuCode != "code-pr" {
		t.Errorf("响应回填缺省时 feishu_code 应回退入参 code，实际 %q", def.FeishuCode)
	}
}
