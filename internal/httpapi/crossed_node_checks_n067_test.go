package httpapi

// N-067 ①：无待办节点上的 approval(<node_id>) 判据 —— 跨节点钩子（第三处承载口径）。
//
// ★ 真实 spec 上本处零行为变化（唯一实例 anti_split_check 的判据 carried_by=manual
// ⇒ 按契约跳过）⇒ 鉴别力由**合成用例**承担（内存副本 mutate、不落盘）。
// ★ 先红后绿：用例 ① 在钩子实现之前必红（approve 放行 200、期望 400 点名）。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// addGhostCrossedCheck 给表单挂一条合成的 approval(<nodeID>) hard 判据（未注册 id
// ⇒ 走 fail-closed；carried_by 由参数控制以覆盖 manual 跳过分支）。
func addGhostCrossedCheck(b *specload.Bundle, docType, nodeID, carriedBy string) {
	form, ok := b.Forms[docType]
	if !ok {
		panic("合成检查：缺表单 " + docType)
	}
	form.Checks = append(form.Checks, specload.CheckDoc{
		ID: "ghost_crossed_check", Severity: "hard",
		When: "approval(" + nodeID + ")", CarriedByKind: carriedBy,
	})
	b.Forms[docType] = form // map 元素是副本 ⇒ 写回
}

// firstReleasedTask 取指定节点已释放的 PENDING 任务（无则 nil）。
func firstReleasedTask(t *testing.T, db *store.DB, bizNo, nodeID string) *store.FlowTask {
	t.Helper()
	tasks, err := db.ListFlowTasks(context.Background(), bizNo)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tasks {
		if tasks[i].NodeID == nodeID && tasks[i].Status == flow.TaskPending &&
			tasks[i].ReleaseState == flow.ReleaseReleased {
			return &tasks[i]
		}
	}
	return nil
}

// TestCrossedNodeCheckBlocksUnregisteredN067 用例①：无待办节点（anti_split_check）
// 上挂未注册 hard 判据 ⇒ approve 该节点**之前**的首个待办时跨过它 ⇒ 可见失败 400 点名。
// ★ 该 approve ＝ approve_petty_cash（purchase_tier1 首个物化节点；anti_split 紧随其后）。
func TestCrossedNodeCheckBlocksUnregisteredN067(t *testing.T) {
	e, db, auth := newSubmitM4AppMutate(t, true, func(b *specload.Bundle) {
		addGhostCrossedCheck(b, "BA", "anti_split_check", "code")
	})
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("BA 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)

	tk := firstReleasedTask(t, db, bizNo, "approve_petty_cash")
	if tk == nil {
		t.Fatal("approve_petty_cash 应为首个已释放待办")
	}
	code2, env2 := postApproveOne(t, e, auth, bizNo, tk.AssigneeOpenID, tk.TaskID, map[string]any{})
	if code2 != http.StatusBadRequest {
		t.Fatalf("跨过无待办节点撞未注册判据应 400（可见失败），实为 %d（%s）—— 跨节点钩子未生效？", code2, env2.Message)
	}
	if !strings.Contains(env2.Message, "ghost_crossed_check") ||
		!strings.Contains(env2.Message, "未注册求值器") {
		t.Errorf("须点名判据与根因, got: %s", env2.Message)
	}
}

// TestCrossedNodeManualSkippedN067 用例③：同位置但 carried_by_kind=manual ⇒ 引擎跳过、
// approve 照常 200（人工承载不被误伤、亦不 fail-closed）。
func TestCrossedNodeManualSkippedN067(t *testing.T) {
	e, db, auth := newSubmitM4AppMutate(t, true, func(b *specload.Bundle) {
		addGhostCrossedCheck(b, "BA", "anti_split_check", "manual")
	})
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("BA 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)

	tk := firstReleasedTask(t, db, bizNo, "approve_petty_cash")
	if tk == nil {
		t.Fatal("approve_petty_cash 应为首个已释放待办")
	}
	code2, env2 := postApproveOne(t, e, auth, bizNo, tk.AssigneeOpenID, tk.TaskID, map[string]any{})
	if code2 != http.StatusOK {
		t.Fatalf("manual 判据必须跳过（不 fail-closed）：approve 应 200, 实为 %d（%s）", code2, env2.Message)
	}
}

// TestCrossedNodeBoundaryNoPredNoFireN067 边界：record（首节点、之前无任何物化节点）
// 的跨过时刻落在提交期（契约 ☆ 本期不定义）⇒ 钩子**任何一次 approve 都不得**触发它
// （否则＝每次都扫全链）。合成 ghost 挂 record ⇒ 全程 approve 仍 200。
func TestCrossedNodeBoundaryNoPredNoFireN067(t *testing.T) {
	e, db, auth := newSubmitM4AppMutate(t, true, func(b *specload.Bundle) {
		addGhostCrossedCheck(b, "BA", "record", "code")
	})
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("BA 提交应 200, 实为 %d（%s）", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)

	// 首个 approve（跨过的是 anti_split_check，不是 record）⇒ record 的 ghost 不得入判据面。
	tk := firstReleasedTask(t, db, bizNo, "approve_petty_cash")
	if tk == nil {
		t.Fatal("approve_petty_cash 应为首个已释放待办")
	}
	code2, env2 := postApproveOne(t, e, auth, bizNo, tk.AssigneeOpenID, tk.TaskID, map[string]any{})
	if code2 != http.StatusOK {
		t.Fatalf("boundary：record 的判据不得在此刻触发（应 200）, 实为 %d（%s）", code2, env2.Message)
	}
}
