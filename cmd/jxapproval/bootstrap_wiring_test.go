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
