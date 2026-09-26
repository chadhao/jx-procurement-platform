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

// approvalEventTypes 需注册的审批事件类型（同时覆盖 2.0 与 1.0 两种键名，增强兼容性）。
var approvalEventTypes = []string{
	"approval.instance.status_changed_v4",
	"approval.task.status_changed_v4",
	"approval.approval.updated_v4",
	"approval_instance",
	"approval_task",
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
	for _, et := range approvalEventTypes {
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
