package feishu

import (
	"context"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// callback_repair_test.go —— 回调链路修复第 2 批（docs/16 §2-B / §2-C 双 code 池）行为测试。

// TestBuildSnapshotWritesActionContext 快照为每个 RELEASED task 写入压缩 JSON
// `{"biz_no":…,"task_id":…}`（与回调解侧同批约定，docs/16 §2-B）；未释放 task 整体省略。
func TestBuildSnapshotWritesActionContext(t *testing.T) {
	inst := &store.Instance{
		BizNo: "PR-2609-0001", ApprovalCode: "code-pr", InstanceCode: "app:PR-2609-0001",
		UpdateTime: 3, Status: "PENDING",
	}
	tasks := []store.FlowTask{
		{TaskID: "t1", NodeID: "n1", AssigneeOpenID: "ou_a", Status: "PENDING", ReleaseState: "RELEASED"},
		{TaskID: "t2", NodeID: "n2", AssigneeOpenID: "ou_b", Status: "PENDING", ReleaseState: "HELD"},
	}
	snap, err := BuildSnapshot(inst, tasks, nil, "https://jx.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.TaskList) != 1 {
		t.Fatalf("快照应仅含 RELEASED 的 t1，实际 %d 条", len(snap.TaskList))
	}
	want := `{"biz_no":"PR-2609-0001","task_id":"t1"}`
	if snap.TaskList[0].ActionContext != want {
		t.Errorf("action_context = %q, 期望 %q", snap.TaskList[0].ActionContext, want)
	}
}

// TestPushPrefersFeishuCode 推实例的 snap.ApprovalCode 优先取 t_approval_def.feishu_code
// （POST 响应回填值候选）、为空回退 approval_code（docs/16 G-8；V-4 归属未实测前双写不猜）。
func TestPushPrefersFeishuCode(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)
	seedInstance(t, db, "PR-1", 5)
	// 定义：主键＝自定义 code-pr-custom；feishu_code＝响应回填值 code-real。
	if err := db.UpsertApprovalDef(ctx, &store.ApprovalDef{
		ApprovalCode: "code-pr-custom", FeishuCode: "code-real", DocType: "PR", Name: "采购申请",
	}); err != nil {
		t.Fatal(err)
	}
	// 实例指向自定义 code（提交侧口径）。
	inst, err := db.GetInstanceByBizNo(ctx, "PR-1")
	if err != nil {
		t.Fatal(err)
	}
	inst.ApprovalCode = "code-pr-custom"
	if err := db.UpsertInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	client := NewFakePushClient()
	p := NewPusher(db, client, "https://jx.example.com", nil)
	if _, err := p.Push(ctx, "PR-1"); err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	snap, _ := client.Last()
	if snap.ApprovalCode != "code-real" {
		t.Errorf("推实例应优先取 feishu_code，实际 %q", snap.ApprovalCode)
	}

	// 回退：feishu_code 为空 ⇒ 用自定义 approval_code。
	//（注：upsert 的 COALESCE 语义是「空值保留旧值」，故此处直改库清除 feishu_code。）
	if _, err := db.ExecContext(ctx,
		`UPDATE t_approval_def SET feishu_code='' WHERE approval_code='code-pr-custom'`); err != nil {
		t.Fatal(err)
	}
	// update_time 未变会被跳过（Skipped）——先推进实例版本再推。
	inst.UpdateTime = 6
	inst.Status = "PENDING"
	if err := db.UpsertInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Push(ctx, "PR-1"); err != nil {
		t.Fatalf("二次推送失败: %v", err)
	}
	snap, _ = client.Last()
	if snap.ApprovalCode != "code-pr-custom" {
		t.Errorf("feishu_code 为空应回退 approval_code，实际 %q", snap.ApprovalCode)
	}
}
