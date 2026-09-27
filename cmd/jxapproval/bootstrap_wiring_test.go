package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
)

// approvalEventKeys ③ 下**不得**再被我方订阅、也不得交给 sink 处理的审批事件键（N10 / F1）。
//
// 覆盖 2.0 与 1.0 两种键名（与 longconn 的 retiredApprovalEventTypes 对应）。
var approvalEventKeys = []string{
	"approval.instance.status_changed_v4",
	"approval.task.status_changed_v4",
	"approval.approval.updated_v4",
	"approval_instance",
	"approval_task",
}

// TestNoApprovalEventSubscription 断言：bootstrap 的订阅目标集合里没有任何审批事件键。
//
// ★ 为什么必须守：③ 下我方是唯一状态源；再订阅飞书审批事件，会与旧事件链一起把
//
//	已推进的终态覆盖回 PENDING（R18/R23）。此测试在"有人把审批事件接回订阅表"时转红。
func TestNoApprovalEventSubscription(t *testing.T) {
	for _, key := range approvalEventKeys {
		for _, target := range subscribeTargetCodes {
			if target == key {
				t.Fatalf("bootstrap 订阅目标 `subscribeTargetCodes` 不得包含审批事件键 %q（③：N10/F1）", key)
			}
		}
	}
}

// TestOldReconcilerNotWiredInBootstrap 断言：bootstrap **不再装配/调度**旧对账器（R23）。
//
// ★ 为什么用源码扫描而非"当前是否被调用"：这是"装配纪律"约束——要保证 bootstrap.go 里
//
//	彻底不再出现旧对账器的构造、调度与"订阅全部审批模板"的符号，而不仅是本次没被调用。
//	一旦有人把旧装配接回来，本测试立即转红。
func TestOldReconcilerNotWiredInBootstrap(t *testing.T) {
	src := readPackageSource(t, "bootstrap.go")
	forbidden := []string{
		"sync.NewReconciler", // 旧对账器构造（R23）
		"sync.NewScheduler",  // 旧对账调度器构造（R23）
		"scheduler.Run",      // 旧定时对账运行点（R23）
		"SubscribeAll(",      // 按全部 approval_code 订阅（③ 下不得再订审批事件，N10/F1）
	}
	for _, sym := range forbidden {
		if strings.Contains(src, sym) {
			t.Fatalf("bootstrap.go 不得再出现旧路径装配符号 %q —— "+
				"R23：旧对账补拉会覆盖我方已推进状态；N10/F1：审批事件不再订阅", sym)
		}
	}
}

// TestLongConnDoesNotRouteApprovalEventsToSink 断言：长连接不把审批事件交给我方 EventSink。
//
// ★ ③ 下审批事件只做 no-op 200（防飞书重试风暴 S8/R07），绝不进入我方事件处理链（N10/F1）。
func TestLongConnDoesNotRouteApprovalEventsToSink(t *testing.T) {
	routed := feishu.SinkRoutedEventTypes()
	for _, key := range approvalEventKeys {
		for _, et := range routed {
			if et == key {
				t.Fatalf("长连接不得把审批事件 %q 交给 EventSink（③：N10/F1）", key)
			}
		}
	}
}

// TestReconcileRouteRetiredGone 断言：旧审批对账入口 `/internal/sync/reconcile` 的处理器
// 已**显式退役**（HTTP 410 Gone + 指明权威入口），且不再触发旧对账器。
//
// ★ 为什么必须守（B17：假入口 / R18·P23：看起来能跑但语义已错）：
//
//	· 若处理器退回 `d.Reconciler.Run(...)`：`Deps.Reconciler` 现为零值 nil → **空指针 500**，
//	  而真实语义是"入口已退役"（不可诊断）；
//	· 若有人只加 nil 守卫返回 501/200：会留下一个"看起来还能用"的**假入口**。
//	故以源码扫描断言：`handlers_ops.go` 的处理器必含 410 与权威入口、且不含旧调用。
func TestReconcileRouteRetiredGone(t *testing.T) {
	src := readPackageSource(t, "../../internal/httpapi/handlers_ops.go")
	if !strings.Contains(src, "http.StatusGone") {
		t.Fatalf("handleReconcile 必须显式返回 http.StatusGone（410）—— 见 docs/11 §4.1 / R24")
	}
	if !strings.Contains(src, "POST /internal/approval/check") {
		t.Fatalf("handleReconcile 的 410 body 必须指明权威入口 POST /internal/approval/check")
	}
	if strings.Contains(src, "d.Reconciler.Run") {
		t.Fatalf("handleReconcile 不得再调用 d.Reconciler.Run —— 旧对账器已随 R23 退役")
	}
}

// TestApprovalCoreWiredInBootstrap 断言转向 ③ 的审批核心已**上电**（R09）。
//
// ★ 为什么必须守：此前 `flow` / `approval` / `number` 三包**零生产装配、仅测试可达** ——
// 所有正确性都跑在死代码路径上（R09）。本测试锁定"三包进入 bootstrap 生产装配"这一事实。
func TestApprovalCoreWiredInBootstrap(t *testing.T) {
	src := readPackageSource(t, "bootstrap.go")
	required := []string{
		"internal/flow",               // flow 领域服务
		"internal/approval",           // 三方审批定义注册表
		"internal/number",             // 单号装配自检
		"flow.NewWithConfig",          // 构造 flow.Service
		"approval.NewRegistry",        // 构造定义注册表
		"feishu.NewPusher",            // 出方向推送服务
		"sync.NewApprovalReconciler",  // 新审批对账器（T03）
		"go approvalRec.Run(ctx)",     // 对账循环独立 goroutine
		"SetCallbackAdvancer",         // 回调异步推进端口
		"feishu.NewCardRefresher",     // 本批：推进成功后卡片主动刷新（实测平台不自动刷）
		"flowCardRefreshSubscriber",   // 本批：卡片刷新订阅者接线
		"ListInstancesByApprovalCode", // 本批：check 入参 instances[] 数据源（实测 99992402 修复）
		"feishu.BuildCheckInstance",   // 本批：check 入参组装（与推送侧同口径）
	}
	for _, sym := range required {
		if !strings.Contains(src, sym) {
			t.Errorf("bootstrap.go 缺少审批核心装配符号 %q（R09：三包须进入生产装配）", sym)
		}
	}
}

// readPackageSource 读取本包目录下的源文件内容。
//
// 说明：`go test` 运行时工作目录＝被测包目录（cmd/jxapproval），故可直接读包内文件名。
func readPackageSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return string(b)
}
