package approval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/approval"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

func sampleDef() approval.DefInput {
	return approval.DefInput{
		DocType:          "PR",
		ApprovalCode:     "code-pr",
		Name:             "采购申请",
		GroupName:        "采购",
		CreateLinkPC:     "https://jx.example/approval/submit?type=PR",
		CreateLinkMobile: "https://jx.example/m/approval/submit?type=PR",
		SupportPC:        true,
		SupportMobile:    true,
		CallbackURL:      "https://jx.example/approval/external/callback",
		CallbackToken:    "tok-pr",
		CallbackKey:      "key-pr",
	}
}

// TestRegistryRepeatRegistrationIsUpdate ★ 重复注册 = 更新（不产生第二条定义）。
func TestRegistryRepeatRegistrationIsUpdate(t *testing.T) {
	db := storetest.NewDB(t)
	fake := feishu.NewFakeExternalApprovalClient()
	reg := approval.NewRegistry(db, fake, nil)
	ctx := context.Background()

	first, err := reg.Register(ctx, sampleDef())
	if err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	if !first.Created {
		t.Errorf("首次注册应标记 Created=true")
	}
	second, err := reg.Register(ctx, sampleDef())
	if err != nil {
		t.Fatalf("二次注册失败: %v", err)
	}
	if second.Created {
		t.Errorf("二次注册应为更新（Created=false）")
	}

	// 本地仅一条定义（负向断言：不得产生第二条）。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_approval_def WHERE doc_type='PR'`); n != 1 {
		t.Errorf("本地定义条数 = %d, 期望 1（重复注册不得产生第二条定义）", n)
	}
	// 飞书侧亦仅一条。
	if n := fake.DefCount(); n != 1 {
		t.Errorf("飞书侧定义条数 = %d, 期望 1", n)
	}
	// def_version 递增（可追溯更新次数）。
	def, err := db.GetApprovalDef(ctx, "code-pr")
	if err != nil {
		t.Fatal(err)
	}
	if def.DefVersion != 2 {
		t.Errorf("def_version = %d, 期望 2", def.DefVersion)
	}
}

// TestRegistryFailureVisible ★ 失败必须可见：飞书失败 → 返回错误，且不写本地行。
func TestRegistryFailureVisible(t *testing.T) {
	db := storetest.NewDB(t)
	fake := feishu.NewFakeExternalApprovalClient()
	fake.UpsertFn = func(_ context.Context, _ feishu.ExternalApprovalDef) (feishu.ExternalApprovalResult, error) {
		return feishu.ExternalApprovalResult{}, errors.New("飞书 500")
	}
	reg := approval.NewRegistry(db, fake, nil)

	if _, err := reg.Register(context.Background(), sampleDef()); err == nil {
		t.Fatalf("飞书失败必须返回错误（不得静默）")
	}
	// 不得制造「本地有、飞书无」的两处真相。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_approval_def`); n != 0 {
		t.Errorf("飞书失败时不得写本地定义行，实际 %d 条", n)
	}
}

// TestRegistryValidation 入参缺失必须显式失败。
func TestRegistryValidation(t *testing.T) {
	db := storetest.NewDB(t)
	reg := approval.NewRegistry(db, feishu.NewFakeExternalApprovalClient(), nil)
	ctx := context.Background()

	for _, in := range []approval.DefInput{
		{DocType: "", ApprovalCode: "c", Name: "n"},
		{DocType: "PR", ApprovalCode: "", Name: "n"},
		{DocType: "PR", ApprovalCode: "c", Name: ""},
	} {
		if _, err := reg.Register(ctx, in); err == nil {
			t.Errorf("非法入参（%+v）应返回错误", in)
		}
	}
}

// TestSyncAggregatesFailures 批量同步：单条失败不阻断其余，但整体报错可见。
func TestSyncAggregatesFailures(t *testing.T) {
	db := storetest.NewDB(t)
	reg := approval.NewRegistry(db, feishu.NewFakeExternalApprovalClient(), nil)
	ctx := context.Background()

	good := sampleDef()
	bad := approval.DefInput{DocType: "SA", ApprovalCode: "code-sa"} // 缺 name
	res, err := reg.Sync(ctx, []approval.DefInput{good, bad})
	if err == nil {
		t.Fatalf("存在失败项时 Sync 应返回错误（可见）")
	}
	if res.Failed != 1 {
		t.Errorf("失败计数 = %d, 期望 1", res.Failed)
	}
	// 好的一条仍成功落库。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_approval_def`); n != 1 {
		t.Errorf("本地定义条数 = %d, 期望 1（好的一条应成功）", n)
	}
}

// TestRegistryPersistsCallbackFields 定义的回调字段完整落本地注册表（供回调校验取值）。
func TestRegistryPersistsCallbackFields(t *testing.T) {
	db := storetest.NewDB(t)
	reg := approval.NewRegistry(db, feishu.NewFakeExternalApprovalClient(), nil)
	if _, err := reg.Register(context.Background(), sampleDef()); err != nil {
		t.Fatal(err)
	}
	var (
		url, token, key string
	)
	err := db.QueryRow(`
SELECT COALESCE(callback_url,''), COALESCE(callback_token,''), COALESCE(callback_key,'')
FROM t_approval_def WHERE approval_code = 'code-pr'`).Scan(&url, &token, &key)
	if err != nil {
		t.Fatal(err)
	}
	if url != sampleDef().CallbackURL || token != "tok-pr" || key != "key-pr" {
		t.Errorf("回调字段落库不完整: url=%q token=%q key=%q", url, token, key)
	}
}
