// Package sync 承载事件订阅与对账（M0）。
//
// ★ 架构转向 ③ 后的本包定位：只承载 **external_instances/check 的方向判断对账**（另行排期）；
//
//	旧的实例「对账补拉」器（Reconciler）**已作废，勿恢复**（R23：其补拉会覆盖我方已推进状态）。
package sync

import (
	"context"
	"log/slog"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// Subscriber 负责按 approval_code 逐个调用「订阅审批事件」（FR-M0-03 / FR-M0-04）。
// ★ 没有这一步，飞书一条事件都不会推送，且现象是「静默无数据」。
type Subscriber struct {
	db     *store.DB
	client feishu.Client
	maps   *config.Maps
	m      *observ.Metrics
	log    *slog.Logger
	now    func() time.Time
}

// NewSubscriber 构造订阅器。
func NewSubscriber(db *store.DB, client feishu.Client, maps *config.Maps, m *observ.Metrics, log *slog.Logger) *Subscriber {
	if m == nil {
		m = observ.NewMetrics()
	}
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Subscriber{db: db, client: client, maps: maps, m: m,
		log: observ.WithComponent(log, "subscribe"), now: func() time.Time { return time.Now().UTC() }}
}

// SubscribeAll 遍历配置表中的全部 approval_code 逐个订阅。
// 返回各 code 的结果与失败的 code 列表（失败需告警，但不影响进程继续运行）。
func (s *Subscriber) SubscribeAll(ctx context.Context) (map[string]feishu.SubscribeResult, []string) {
	var codes []string
	if s.maps != nil && s.maps.Approval != nil {
		codes = s.maps.Approval.Codes()
	}
	return s.Subscribe(ctx, codes)
}

// Subscribe 订阅指定 approval_code 列表（空列表返回空结果）。
func (s *Subscriber) Subscribe(ctx context.Context, codes []string) (map[string]feishu.SubscribeResult, []string) {
	results := make(map[string]feishu.SubscribeResult, len(codes))
	var failed []string
	for _, code := range codes {
		res, err := s.client.SubscribeApprovalEvent(ctx, code)
		now := s.now()
		st := store.SubscribeState{
			ApprovalCode:  code,
			Subscribed:    err == nil && res.Subscribed,
			LastResult:    res.Result,
			LastAttemptAt: &now,
			UpdatedAt:     now,
		}
		if s.maps != nil && s.maps.Approval != nil {
			if dt, ok := s.maps.Approval.DocType(code); ok {
				st.DocType = dt
			}
		}
		if err != nil {
			st.LastError = err.Error()
			failed = append(failed, code)
			s.log.Error("订阅失败（★ 漏订阅将导致静默无数据，需告警）",
				"approval_code", code, "error", err.Error())
		} else {
			s.log.Info("订阅成功", "approval_code", code, "doc_type", st.DocType)
		}
		if upsertErr := s.db.UpsertSubscribeState(ctx, st); upsertErr != nil {
			s.log.Error("写入订阅状态失败", "approval_code", code, "error", upsertErr.Error())
		}
		results[code] = res
	}
	return results, failed
}
