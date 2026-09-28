package feishu

import (
	"context"
	"log/slog"
	"sync/atomic"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
)

// EventSink 事件落地回调（由 inbox 实现，同步路径只写收件箱并立即返回）。
type EventSink interface {
	HandleEvent(ctx context.Context, payload []byte) error
}

// retiredApprovalEventTypes ③ 下**不再处理**的审批事件类型（同时覆盖 2.0 与 1.0 两种键名）。
//
// ★ 处置：这些键**仍然注册**，但处理器是 no-op（返回 nil → 飞书得到 200）。
//
//	为什么"删了处理却保留注册"：③ 下我方是唯一状态源，审批事件不得再进入我方事件链
//	（N10 / F1）；但若**完全不注册**，飞书对残留审批事件的推送会得到非 200 → 触发
//	**重试风暴**（S8 / R07）。故：既不交给我方 sink，也不让飞书重试。
var retiredApprovalEventTypes = []string{
	"approval.instance.status_changed_v4",
	"approval.task.status_changed_v4",
	"approval.approval.updated_v4",
	"approval_instance",
	"approval_task",
}

// sinkEventTypes 当前**交给 EventSink 处理**的事件类型。
//
// ★ ③ 下审批事件**不在其中**（见 retiredApprovalEventTypes）。
//
// ★ docs/08 批次二（事件增量）已接入：6 个通讯录（部门/人员）变更事件**复用同一长连接**，
// 由 inbox 按 event_type 分流到 orgsync 增量落库（绝不混入审批事件处理链——
// 审批键仍全部在 retiredApprovalEventTypes 的 no-op 名单里，见上方守卫注释）。
// 事件名经官方文档核对（2026-09-28，见 docs/reference/README.md）：
// 6 类均支持「使用长连接接收事件」。
var sinkEventTypes = []string{
	"contact.department.created_v3",
	"contact.department.updated_v3",
	"contact.department.deleted_v3",
	"contact.user.created_v3",
	"contact.user.updated_v3",
	"contact.user.deleted_v3",
}

// SinkRoutedEventTypes 返回当前交给 EventSink 处理的事件类型（副本，供守卫测试断言）。
func SinkRoutedEventTypes() []string {
	return append([]string(nil), sinkEventTypes...)
}

// LongConn 封装飞书长连接 WebSocket（M0）。
//
// ★ 联调状态：使用 larksuite/oapi-sdk-go/v3 的 WebSocket 客户端建立长连接，
// 事件以原始报文形态（req.Body）转发给 EventSink；SDK 负责保活与自动重连（FR-M0-02 / TC-29）。
type LongConn struct {
	appID     string
	appSecret string
	sink      EventSink
	log       *slog.Logger

	connected atomic.Bool
	cli       *larkws.Client
}

// NewLongConn 构造长连接客户端。
func NewLongConn(appID, appSecret string, sink EventSink, log *slog.Logger) *LongConn {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &LongConn{
		appID:     appID,
		appSecret: appSecret,
		sink:      sink,
		log:       observ.WithComponent(log, "longconn"),
	}
}

// Connected 返回长连接是否已建立（供 /readyz 使用）。
func (l *LongConn) Connected() bool { return l.connected.Load() }

// Run 启动长连接并阻塞，直到 ctx 取消。SDK 内部处理保活与重连。
func (l *LongConn) Run(ctx context.Context) error {
	d := dispatcher.NewEventDispatcher("", "")

	// ① 退役的审批事件：注册 no-op（返回 nil → 200），阻止飞书重试风暴（S8/R07），
	//    但**绝不**把报文交给 sink（我方是唯一状态源，N10/F1）。
	for _, et := range retiredApprovalEventTypes {
		d.OnCustomizedEvent(et, func(ctx context.Context, _ *larkevent.EventReq) error {
			return nil
		})
	}

	// ② 仍需我方处理的事件（③ 下审批事件不在其中；通讯录事件已接入，交 inbox 分流 orgsync）。
	for _, et := range sinkEventTypes {
		eventType := et
		d.OnCustomizedEvent(eventType, func(ctx context.Context, req *larkevent.EventReq) error {
			// ★ 同步路径纪律：这里只把报文交给 inbox，绝不在此调用飞书 API 或 sleep。
			return l.sink.HandleEvent(ctx, req.Body)
		})
	}

	l.cli = larkws.NewClient(l.appID, l.appSecret,
		larkws.WithEventHandler(d),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithOnReady(func() {
			l.connected.Store(true)
			l.log.Info("长连接已建立")
		}),
		larkws.WithOnReconnected(func() {
			l.connected.Store(true)
			l.log.Info("长连接已重连")
		}),
		larkws.WithOnReconnecting(func() {
			l.connected.Store(false)
			l.log.Warn("长连接重连中")
		}),
		larkws.WithOnDisconnected(func() {
			l.connected.Store(false)
			l.log.Warn("长连接已断开")
		}),
		larkws.WithOnError(func(err error) {
			l.log.Error("长连接错误", "error", err)
		}),
	)

	l.log.Info("启动飞书长连接", "app_id", l.appID)
	return l.cli.Start(ctx)
}

// Stop 请求停止长连接（优雅退出时先停长连接，再排空 worker）。
func (l *LongConn) Stop() {
	if l.cli != nil {
		l.cli.Close()
	}
}
