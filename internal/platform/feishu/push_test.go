package feishu

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

var pushAt = time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)

// TestChooseUpdateMode 选型判据：能 UPDATE 就不用 REPLACE；REPLACE 仅限首推 / 需删。
func TestChooseUpdateMode(t *testing.T) {
	if got := ChooseUpdateMode(false, false); got != UpdateModeUpdate {
		t.Errorf("非首推、无需删 → %s, 期望 UPDATE", got)
	}
	if got := ChooseUpdateMode(true, false); got != UpdateModeReplace {
		t.Errorf("首推 → %s, 期望 REPLACE", got)
	}
	if got := ChooseUpdateMode(false, true); got != UpdateModeReplace {
		t.Errorf("需删 → %s, 期望 REPLACE", got)
	}
}

// TestBuildSnapshotOnlyReleasedLimits 快照只含 RELEASED；超限报错不截断；
// detailBase 为空 ⇒ 可见失败（links 必填，2026-09-28 实测 99992402）。
func TestBuildSnapshotOnlyReleasedLimits(t *testing.T) {
	inst := &store.Instance{ApprovalCode: "code-pr", InstanceCode: "app:PR-1", UpdateTime: 3, Status: "PENDING"}
	tasks := []store.FlowTask{
		{TaskID: "t1", NodeID: "n1", AssigneeOpenID: "ou_a", Status: "PENDING", ReleaseState: "RELEASED"},
		{TaskID: "t2", NodeID: "n1", AssigneeOpenID: "ou_b", Status: "PENDING", ReleaseState: "HELD"},
	}
	snap, err := BuildSnapshot(inst, tasks, nil, "https://jx.example.com", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.TaskList) != 1 || snap.TaskList[0].TaskID != "t1" {
		t.Errorf("快照 task_list = %+v, 期望仅 t1（只含 RELEASED）", snap.TaskList)
	}
	// detailBase 为空 ⇒ 可见失败（links 必填；不编造 URL）。
	if _, err := BuildSnapshot(inst, tasks, nil, "  ", "", nil); err == nil {
		t.Errorf("detailBase 为空应报错（links 必填，不编造 URL）")
	}
	// 超限：301 个 RELEASED。
	var many []store.FlowTask
	for i := 0; i < MaxTaskList+1; i++ {
		many = append(many, store.FlowTask{TaskID: "x", ReleaseState: "RELEASED"})
	}
	if _, err := BuildSnapshot(inst, many, nil, "https://jx.example.com", "", nil); err == nil {
		t.Errorf("task_list 超限应报错（绝不静默截断）")
	}
}

// seedInstance 建一条实例 + 两个任务（1 RELEASED + 1 HELD）。
func seedInstance(t *testing.T, db *store.DB, bizNo string, updateTime int64) {
	t.Helper()
	ctx := context.Background()
	if err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "app:" + bizNo, ApprovalCode: "code-pr", DocType: "PR", BizNo: bizNo,
		Status: "PENDING", UpdateTime: updateTime, CreatedAt: pushAt, UpdatedAt: pushAt,
		ApplicantOpenID: "ou_user",
	}); err != nil {
		t.Fatal(err)
	}
	tasks := []store.FlowTask{
		{TaskID: bizNo + "-n1-ou_a-1-1", BizNo: bizNo, NodeID: "n1", NodeSeq: 1, Round: 1,
			NodeName:       "部门负责人审批",
			AssigneeOpenID: "ou_a", Status: "PENDING", ReleaseState: "RELEASED", TaskOrder: 1,
			CreatedAt: pushAt, UpdatedAt: pushAt},
		{TaskID: bizNo + "-n2-ou_b-1-2", BizNo: bizNo, NodeID: "n2", NodeSeq: 2, Round: 1,
			AssigneeOpenID: "ou_b", Status: "PENDING", ReleaseState: "HELD", TaskOrder: 1,
			CreatedAt: pushAt, UpdatedAt: pushAt},
	}
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		for i := range tasks {
			if err := db.UpsertFlowTaskTx(ctx, tx, &tasks[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPusherFlow 首推 REPLACE → 版本未增跳过 → 版本增后 UPDATE；写 t_push_record=SENT。
func TestPusherFlow(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	bizNo := "PR-2609-0001"
	seedInstance(t, db, bizNo, 1)

	fc := NewFakePushClient()
	p := NewPusher(db, fc, "https://jx.example.com", nil)

	// ① 首推 → REPLACE，快照 1 个 RELEASED。
	res, err := p.Push(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || res.UpdateMode != UpdateModeReplace {
		t.Fatalf("首推 = %+v, 期望 Pushed/REPLACE", res)
	}
	snap, _ := fc.Last()
	if len(snap.TaskList) != 1 {
		t.Errorf("首推快照 task_list = %d, 期望 1（只含 RELEASED）", len(snap.TaskList))
	}
	if n, _ := db.CountPushRecords(ctx, bizNo); n != 1 {
		t.Errorf("推送流水 = %d, 期望 1", n)
	}
	recs, _ := db.ListPushRecords(ctx, bizNo)
	if len(recs) != 1 || recs[0].Status != store.PushSent {
		t.Errorf("推送流水状态 = %+v, 期望 SENT", recs)
	}

	// ② update_time 未增 → 跳过（幂等）。
	res2, err := p.Push(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped || res2.Pushed {
		t.Errorf("版本未增应 Skipped，实际 %+v", res2)
	}
	if fc.Count() != 1 {
		t.Errorf("跳过不应再推，推送次数 = %d", fc.Count())
	}

	// ③ update_time 增 → UPDATE。
	if err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "app:" + bizNo, ApprovalCode: "code-pr", DocType: "PR", BizNo: bizNo,
		Status: "PENDING", UpdateTime: 2, CreatedAt: pushAt, UpdatedAt: pushAt,
	}); err != nil {
		t.Fatal(err)
	}
	res3, err := p.Push(ctx, bizNo)
	if err != nil {
		t.Fatal(err)
	}
	if res3.UpdateMode != UpdateModeUpdate {
		t.Errorf("非首推应 UPDATE，实际 %s", res3.UpdateMode)
	}
}

// TestPusherPushFailureRecorded 推送失败 → 流水 FAILED（可检出，不静默）。
func TestPusherPushFailureRecorded(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	bizNo := "PR-2609-0002"
	seedInstance(t, db, bizNo, 1)
	fc := NewFakePushClient()
	fc.PushFn = func(context.Context, string, InstanceSnapshot) error {
		return context.DeadlineExceeded
	}
	p := NewPusher(db, fc, "https://jx.example.com", nil)
	if _, err := p.Push(ctx, bizNo); err == nil {
		t.Fatal("推送失败应返回错误")
	}
	recs, _ := db.ListPushRecords(ctx, bizNo)
	if len(recs) != 1 || recs[0].Status != store.PushFailed {
		t.Errorf("推送流水 = %+v, 期望 FAILED", recs)
	}
}
