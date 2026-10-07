package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// callback_contract_test.go —— #69 ① 的**契约探针**（HTTP 级）。
//
// 场景：回调「**落盘成功但异步推进失败**」（`CallbackResult.Accepted==true` + err≠nil）。
//   - 改前：handler 把它送进 500；
//   - 改后（#69 ①）：**已受理即 200**（用户口径 D2）。
// 同时断言：**推进确实没有发生**（任务仍 PENDING）—— 这正是 ② 的修复循环存在的理由。

// newCallbackProbeApp 装配一个挂了真实 `flow.Service` 的路由（仅回调路径会被打到）。
func newCallbackProbeApp(t *testing.T, svc *flow.Service, db *store.DB) *echo.Echo {
	t.Helper()
	return NewRouter(Deps{
		Env:     &config.Env{DevMode: true},
		DB:      db,
		Log:     observ.NewLogger("error", io.Discard),
		Metrics: observ.NewMetrics(),
		Health:  observ.NewHealth("test"),
		Flow:    svc,
		Auth:    access.NewAuthenticator(db, access.NewStore(db, "probe-key", time.Hour), nil, true, nil),
		Maps:    &config.Maps{},
	})
}

// seedCallbackProbeInstance 预置定义 + 提交一张单节点单据，返回 (bizNo, taskID)。
func seedCallbackProbeInstance(t *testing.T, svc *flow.Service, db *store.DB) (string, string) {
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
		Nodes: []flow.NodeSpec{{NodeID: "n1", NodeName: "主管", Seq: 1,
			Approvers: []flow.Approver{{OpenID: "ou_m1", Name: "李四"}}}},
		At: time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	tasks, err := db.ListFlowTasks(ctx, bizNo)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("读取任务失败: %v (n=%d)", err, len(tasks))
	}
	return bizNo, tasks[0].TaskID
}

// TestCallbackAcceptedReturns200EvenWhenAdvanceFails —— #69 ① 契约探针。
func TestCallbackAcceptedReturns200EvenWhenAdvanceFails(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)
	svc := flow.New(db, "app")
	bizNo, taskID := seedCallbackProbeInstance(t, svc, db)

	// 注入**必然失败**的 advancer：模拟「落盘成功、推进失败」。
	svc.SetCallbackAdvancer(func(ctx context.Context, req flow.CallbackRequest) error {
		return errors.New("探针：模拟推进失败")
	})

	e := newCallbackProbeApp(t, svc, db)
	body := `{"action_name":"APPROVE","biz_no":"` + bizNo + `","task_id":"` + taskID +
		`","token":"tok-pr","operator":{"open_id":"ou_m1"}}`
	rec, env := doRequest(e, http.MethodPost, "/approval/external/callback", "", body)

	// ★ 契约：已落盘 ⇒ 200（改前＝500）。
	if rec.Code != http.StatusOK {
		t.Fatalf("已受理回调应回 200（#69 ①），实际 http=%d body=%s", rec.Code, rec.Body.String())
	}
	if env.Code != codeOK {
		t.Fatalf("已受理回调应 ok 包裹（code=0），实际 code=%d", env.Code)
	}

	// ★★ 断言「推进**确实没发生**」——故必须有 ② 的修复循环兜底，否则即是静默卡死。
	if inst, _ := db.GetInstanceByBizNo(ctx, bizNo); inst == nil || inst.Status != flow.InstancePending {
		got := "<nil>"
		if inst != nil {
			got = inst.Status
		}
		t.Fatalf("advancer 失败时实例应仍 PENDING，实际 %s", got)
	}
	if task, _ := db.GetFlowTask(ctx, taskID); task == nil || task.Status != flow.TaskPending {
		got := "<nil>"
		if task != nil {
			got = task.Status
		}
		t.Fatalf("advancer 失败时任务应仍 PENDING，实际 %s", got)
	}

	// 落盘**已发生**（op_log 有 1 条 APPROVE）——正是「已受理」的依据、也是修复循环的锚点。
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_flow_op_log WHERE biz_no=? AND task_id=? AND op_type='APPROVE'`,
		bizNo, taskID); n != 1 {
		t.Fatalf("落盘 APPROVE 留痕应为 1（已受理），实际 %d", n)
	}
}
