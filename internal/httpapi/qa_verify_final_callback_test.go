package httpapi

// qa_verify_final_callback_test.go —— 独立验证者（qa-verify-final）验证测试**收编入库**版（HTTP 级）。
//
// 来源与依据：
//   - 首轮独立验证（qa-verify-final，HEAD=25066a0）：#69 ①「落盘即 200」契约用 REJECT 键
//     独立构景（作者探针只用 APPROVE）。
//   - 新增两守门断言（56d6138，#62 残留半边两缺口）：HELD 抢跑回调 → 409（非 200/500）＋
//     不占键；定义缺失 → 409（绝不得 500/50000）。定案 #62 / #69 / #77。
//   - ★ ⑤a 翻转：原「HELD 带留痕」观察探针已翻转为本文件与 flow 包的负向回归断言。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// TestQAVFCallbackRejectAcceptedReturns200EvenWhenAdvanceFails —— #69 ① 契约（REJECT 键）。
//
// 为什么重要：「已落盘 ⇒ 200」对**两键**都成立才算契约；只测 APPROVE 会漏掉 REJECT 侧
// handler 分支的回归。
func TestQAVFCallbackRejectAcceptedReturns200EvenWhenAdvanceFails(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	// 注入必然失败的 advancer：模拟「落盘成功、推进失败」。
	svc.SetCallbackAdvancer(func(ctx context.Context, req flow.CallbackRequest) error {
		return errors.New("qa-vf: 模拟推进失败")
	})

	e := newCallbackProbeApp(t, svc, db)
	body := `{"action_name":"REJECT","biz_no":"` + bizNo + `","task_id":"` + taskID +
		`","token":"tok-pr","reason":"不符合要求","operator":{"open_id":"ou_m1"}}`
	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)

	// ★ 契约：已落盘 ⇒ 200（不是 500）。
	if rec.Code != http.StatusOK {
		t.Fatalf("REJECT 已落盘应回 200（#69 ①），实际 http=%d body=%s", rec.Code, rec.Body.String())
	}
	if env.Code != codeOK {
		t.Fatalf("已受理回调应 ok 包裹（code=0），实际 code=%d body=%s", env.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"advance_deferred":true`) {
		t.Errorf("响应应标明推进已延后（advance_deferred=true），实际 body=%s", rec.Body.String())
	}

	// 推进确实未发生：任务/实例仍 PENDING。
	if task, _ := db.GetFlowTask(ctx, taskID); task == nil || task.Status != flow.TaskPending {
		t.Fatalf("advancer 失败时任务应仍 PENDING")
	}
	if inst, _ := db.GetInstanceByBizNo(ctx, bizNo); inst == nil || inst.Status != flow.InstancePending {
		t.Fatalf("advancer 失败时实例应仍 PENDING")
	}
	// 落盘已发生：REJECT 留痕恰 1 条（修复循环的锚点）。
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_flow_op_log WHERE biz_no=? AND task_id=? AND op_type='REJECT'`,
		bizNo, taskID); n != 1 {
		t.Fatalf("落盘 REJECT 留痕应为 1，实际 %d", n)
	}
}

// seedQAVFTwoNodeInstance 预置定义 + 提交两节点单据（n1=ou_m1 → n2=ou_gm），返回 (bizNo, gmTaskID)。
func seedQAVFTwoNodeInstance(t *testing.T, svc *flow.Service, db *store.DB) (string, string) {
	t.Helper()
	ctx := context.Background()
	if err := db.UpsertApprovalDef(ctx, &store.ApprovalDef{
		ApprovalCode: "code-pr", DocType: "PR", Name: "采购申请", CallbackToken: "tok-pr",
	}); err != nil {
		t.Fatalf("预置审批定义失败: %v", err)
	}
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "主管", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_m1", Name: "李四"}}},
			{NodeID: "n2", NodeName: "总经理", Seq: 2, Approvers: []flow.Approver{{OpenID: "ou_gm", Name: "王五"}}},
		},
		At: time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	tasks, err := db.ListFlowTasks(ctx, bizNo)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	for _, tk := range tasks {
		if tk.AssigneeOpenID == "ou_gm" {
			return bizNo, tk.TaskID
		}
	}
	t.Fatalf("未找到 ou_gm 任务（biz_no=%s）", bizNo)
	return "", ""
}

// TestQAVFHeldTaskCallbackReturns409NoKey —— ⑤a 翻转后的 HTTP 级守门断言（56d6138 缺口 1）。
//
// 为什么重要：抢跑回调若回 200/500 或占键，真实回调将被幂等键挡回 → 静默卡死；
// 409 + 不占键 = 与页面路径同一可见拒绝口径（#62）。
func TestQAVFHeldTaskCallbackReturns409NoKey(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, gmTaskID := seedQAVFTwoNodeInstance(t, svc, db)

	e := newCallbackProbeApp(t, svc, db)
	body := `{"action_name":"APPROVE","biz_no":"` + bizNo + `","task_id":"` + gmTaskID +
		`","token":"tok-pr","operator":{"open_id":"ou_gm"}}`
	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)

	// ★ 守门：HELD 抢跑 → 409 / codeApprovalConflict（不是 200 受理、更不是 500）。
	if rec.Code != http.StatusConflict {
		t.Fatalf("★ 守门失守：HELD 抢跑回调应 409，实际 http=%d body=%s", rec.Code, rec.Body.String())
	}
	if env.Code != codeApprovalConflict {
		t.Fatalf("★ 守门失守：业务码应 %d（codeApprovalConflict），实际 %d body=%s",
			codeApprovalConflict, env.Code, rec.Body.String())
	}
	// 不占幂等键（#62：准入失败不落盘）。
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_flow_op_log WHERE biz_no=? AND task_id=? AND op_type='APPROVE'`,
		bizNo, gmTaskID); n != 0 {
		t.Fatalf("★ 守门失守：抢跑回调占用幂等键（留痕 %d 条，期望 0）", n)
	}
	// 任务未被改动。
	if task, _ := db.GetFlowTask(ctx, gmTaskID); task == nil || task.Status != flow.TaskPending ||
		task.ReleaseState != flow.ReleaseHeld {
		t.Fatalf("★ 守门失守：HELD 任务被改动")
	}
}

// TestQAVFDefinitionMissingCallbackReturns409Not500 —— 56d6138 缺口 2 HTTP 级守门断言。
//
// 为什么重要：500/50000 会让飞书把**服务端配置问题**当故障无限重试且掩盖真实原因；
// ErrDefinitionMissing → 409 那行映射在回调路径必须**真正可达**。
func TestQAVFDefinitionMissingCallbackReturns409Not500(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	// 构造「实例在、定义缺失」：提交成功后删除定义（模拟服务端配置丢失）。
	if _, err := db.ExecContext(ctx, `DELETE FROM t_approval_def WHERE approval_code='code-pr'`); err != nil {
		t.Fatalf("删除定义失败: %v", err)
	}

	e := newCallbackProbeApp(t, svc, db)
	body := `{"action_name":"APPROVE","biz_no":"` + bizNo + `","task_id":"` + taskID +
		`","token":"tok-pr","operator":{"open_id":"ou_m1"}}`
	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)

	// ★ 守门：定义缺失 → 409（绝不得 500/50000）。
	if rec.Code != http.StatusConflict {
		t.Fatalf("★ 守门失守：定义缺失回调应 409，实际 http=%d body=%s（改前落 500/50000）", rec.Code, rec.Body.String())
	}
	if env.Code != codeApprovalConflict {
		t.Fatalf("★ 守门失守：业务码应 %d，实际 %d body=%s", codeApprovalConflict, env.Code, rec.Body.String())
	}
	// 未受理 ⇒ 不落盘占键。
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_flow_op_log WHERE biz_no=? AND task_id=? AND op_type='APPROVE'`,
		bizNo, taskID); n != 0 {
		t.Fatalf("★ 守门失守：被拒回调落盘占键（留痕 %d 条，期望 0）", n)
	}
}
