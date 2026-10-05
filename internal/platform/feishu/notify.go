package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// notify.go —— 待办通知发送（flow.Sender 的飞书 Bot 实现；docs/16 G-5 / §2-F 关联项）。
//
// ★★ 铁律（05-API §6.1 / reference/README.md ★★ 实测条目）：
//
//	推 `external_instances` 只让任务进审批中心「待办」，**不发任何通知**；
//	通知必须另行调 `POST /open-apis/approval/v1/message/send`（template_id=1008「收到审批待办」）。
//	此前 `flow.Sender` 装配传 nil ⇒ 通知零实现（审批人在飞书完全看不到提醒，本次实测踩中）。
//
// ★ 实测契约（2026-09-27，可直接采信；本文件实现完全按此对齐）：
//   - `actions[]` 的 **四个 URL 缺一不可**（`url`＋`pc_url`＋`android_url`＋`ios_url`），
//     只给 3 个 ⇒ `60001 actionUrls incomplete error`（★ **HTTP 仍为 200**，须按 body 的
//     `code` 判成败 —— 本实现经 doJSON 统一按 `code != 0` 判失败，天然满足，见 Send 注释）；
//   - 本接口 **`i18n_resources[].texts` 是 map 形态** `{"@i18n@x":"值"}`，与
//     `external_approvals` 要求的数组形态 **相反** ⇒ 绝不跨接口外推（R-1）；
//   - `uuid` 为幂等 ID（同 uuid 一小时内只发一次）—— 本实现用**确定性** uuid
//     （biz_no+event+target），重试/重发天然去重。
//
// ★ 两阶段机制不另起炉灶：本实现只做 `flow.Sender` 的「发送」半边；EXPECT→SENT/FAILED
// 的落盘回填由 `flow.Notifier`（internal/flow/notify.go）既有机制完成（t_notify_log）。
//
// ★ 边界纪律：本包**不反向依赖领域包 flow**（同 push.go 对 flow.InstancePending 的处理）——
// 事件文案用字符串字面量并对齐 flow 事件常量注释；`flow.Sender` 形态由
// cmd/jxapproval/bootstrap.go 的编译期断言保证一致。

// NotifyTemplateTodo 「收到审批待办」（官方模板列表；支持快捷审批参数）。
// 其他相关编号（reference/README.md）：1002 暂存待办 / 1003 已拒绝 / 1004 已通过 / 1005 已发起 /
// 1009 被加签 / 1010 被转交 / 1011 被委托 / 1012 被回退 / 1013 人工催办 / 1015 被撤回 /
// 1016 被抄送 / 1021 自定义 / 1028 待办（无发起人）。
const NotifyTemplateTodo = "1008"

// notifyChannelBot 通知渠道（Bot）。★ feishu 包不反向依赖领域包 flow（本文件头纪律），
// 字面量与 flow.ChannelBot 对齐（bootstrap 装配处编译期断言保证 flow.Sender 形态一致）。
const notifyChannelBot = "feishu_bot"

// NotifySender 通知发送器：内调 `POST /open-apis/approval/v1/message/send`。
//
// 方法集与 `flow.Sender` 形态一致（bootstrap 装配处编译期断言）。
type NotifySender struct {
	db     *store.DB
	client *HTTPClient
	// detailBase 详情页基址（JX_CALLBACK_DOMAIN）：DETAIL 链接＝`<detailBase>/approval/<biz_no>`
	//（web/src/router.js 的 approval-console 路由）。★ 为空 ⇒ 发送**可见失败**（四个 URL
	// 缺一即 60001；绝不静默编造 URL，漏发由 t_notify_log.FAILED 检出）。
	detailBase string
	log        *slog.Logger
	// throttle 配额降级门（N-060 F2 · 01a §5.5 约束3）：true ⇒ 跳过 Bot 通知
	//（降**非关键调用**、保对账）；nil ⇒ 不过滤（默认）。
	throttle func() bool
}

// SetQuotaThrottle 注入配额降级门（bootstrap 装配：>=90% 时跳过 Bot）。
func (s *NotifySender) SetQuotaThrottle(f func() bool) { s.throttle = f }

// NewNotifySender 构造通知发送器（生产装配；开发无凭据场景用 NewFakeNotifySender）。
func NewNotifySender(db *store.DB, client *HTTPClient, detailBase string, log *slog.Logger) *NotifySender {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &NotifySender{
		db: db, client: client,
		detailBase: strings.TrimRight(strings.TrimSpace(detailBase), "/"),
		log:        observ.WithComponent(log, "feishu"),
	}
}

// Send 给 targetOpenID 发一条审批 Bot 消息（通知）。
//
// ★ 事件域（FR-M0-17 接线后两类齐备，均走本方法）：
//   - 「通知已通过者」类（`flow.Notifier` 既有口径：TRANSFERRED / ROLLED_BACK / CANCELED，
//     target＝该单已审批通过者，01a §4.7）—— 按通用「审批状态更新」文案组装；
//   - 「新待办产生」类（`TASK_ACTIVATED:<task_id>`，FR-M0-17；02-UseCase UC-23 通知时机：
//     只有被激活（RELEASED）的任务才发通知）—— target＝新激活任务的 assignee。
//
// ★★ 成败判定铁律：**`code != 0` 一律视为失败**（HTTP 200 不代表成功 —— 实测 60001
// actionUrls incomplete 即 HTTP 200）。本方法经 `HTTPClient.doJSON` 发出，其统一按
// body 的 `code` 判成败（code!=0 返回 error）⇒ 失败会被 flow.Notifier 记为
// t_notify_log 的 FAILED + last_error，**绝不当成功**。
func (s *NotifySender) Send(ctx context.Context, bizNo, targetOpenID, event string) error {
	bizNo = strings.TrimSpace(bizNo)
	target := strings.TrimSpace(targetOpenID)
	if bizNo == "" || target == "" {
		return fmt.Errorf("feishu: 发送待办通知失败: biz_no/open_id 不能为空")
	}
	// ★ N-060 F2（01a §5.5 约束3）：配额 ≥90% ⇒ 降非关键（Bot 通知）、保对账。
	// 返回 nil＝本通道按降级策略不发送（可见 Warn 留痕；站内兜底通道独立、不受此门影响）。
	if s.throttle != nil && s.throttle() {
		if s.log != nil {
			s.log.Warn("★ 配额水位 ≥90%：跳过 Bot 通知（降非关键调用，保对账 —— 01a §5.5 约束3）",
				"biz_no", bizNo, "target", target, "event", event)
		}
		return nil
	}
	if s.client == nil {
		return fmt.Errorf("feishu: 发送待办通知失败: 未配置飞书客户端")
	}
	if s.detailBase == "" {
		// ★ 四个 URL 必须非空（缺一即 60001）；无域名配置宁可可见失败，不编造 URL。
		return fmt.Errorf("feishu: 发送待办通知失败: JX_CALLBACK_DOMAIN 未配置，无法构造「查看详情」链接")
	}
	// ① 定位实例（申请人 = title_user_id；摘要/名称取自实例与定义）。
	inst, err := s.db.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		return fmt.Errorf("feishu: 发送待办通知失败（定位实例 %s）: %w", bizNo, err)
	}
	if strings.TrimSpace(inst.ApplicantOpenID) == "" {
		// title_user_id（申请人）是模板必填语义，空值宁可可见失败也不臆造。
		return fmt.Errorf("feishu: 发送待办通知失败: 实例 %s 无申请人 open_id（title_user_id 无法组装）", bizNo)
	}
	// ② 审批名称：优先三方定义名；定义未注册（ErrNotFound 属正常态）回退单据类型，不吞其余错误。
	name := strings.TrimSpace(inst.DocType)
	if def, derr := s.db.GetApprovalDef(ctx, inst.ApprovalCode); derr == nil && strings.TrimSpace(def.Name) != "" {
		name = strings.TrimSpace(def.Name)
	} else if derr != nil && !isNotFound(derr) {
		s.log.Warn("发送通知前读取审批定义失败（回退单据类型作为审批名称）",
			"biz_no", bizNo, "approval_code", inst.ApprovalCode, "error", derr.Error())
	}
	// ③ DETAIL 链接：四个 URL 同值齐全（实测：缺一即 60001 actionUrls incomplete error）。
	detail := s.detailBase + "/approval/" + bizNo

	// ④ 组装请求体（字段结构＝2026-09-27 实测成功 payload，docs/05-API §6.1）：
	//    i18n 键走 `@i18n@` 占位，texts 为 **map 形态**（本接口与 external_approvals 相反）。
	body := map[string]any{
		"template_id":        NotifyTemplateTodo,
		"open_id":            target,
		"uuid":               s.uuid(bizNo, target, event),
		"approval_name":      "@i18n@name",
		"title_user_id":      inst.ApplicantOpenID,
		"title_user_id_type": "open_id",
		"content": map[string]any{
			"user_id":      inst.ApplicantOpenID,
			"user_id_type": "open_id",
			"summaries": []any{
				map[string]any{"summary": "@i18n@s1"},
				map[string]any{"summary": "@i18n@s2"},
			},
		},
		"actions": []any{
			map[string]any{
				"action_name": "DETAIL",
				"url":         detail,
				"pc_url":      detail,
				"android_url": detail,
				"ios_url":     detail,
			},
		},
		"i18n_resources": []any{
			map[string]any{
				"locale":     "zh-CN",
				"is_default": true,
				// ★ map 形态（实测；与 external_approvals 的数组形态相反，勿跨接口外推）。
				"texts": map[string]any{
					"@i18n@name": name,
					"@i18n@s1":   fmt.Sprintf("单号：%s", bizNo),
					"@i18n@s2":   notifyEventText(event),
				},
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("feishu: 组装 message/send 请求体失败: %w", err)
	}
	// ⑤ 发送：doJSON 统一按 body.code 判成败（HTTP 200 + code!=0 也算失败，见函数注释）。
	data, _, err := s.client.doJSON(ctx, http.MethodPost,
		"/open-apis/approval/v1/message/send", nil, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("feishu: 发送待办通知失败（biz_no=%s target=%s event=%s）: %w", bizNo, target, event, err)
	}
	// 成功回执含 data.message_id（实测 `{"code":0,"data":{"message_id":"…"}}`）——记日志备查。
	// ★ 回执解析失败只告警、不推翻发送成功判定（消息已发出；但绝不静默丢弃错误，C6）。
	var out struct {
		MessageID string `json:"message_id"`
	}
	if len(data) > 0 {
		if uerr := json.Unmarshal(data, &out); uerr != nil {
			s.log.Warn("message/send 已成功但回执解析失败（不影响发送成功判定）",
				"biz_no", bizNo, "target", target, "error", uerr.Error())
		}
	}
	// ★★ 本批新增（0014 列写入者，R26 教训：列必须有真实写入者）：
	//   把回执 data.message_id 落 t_notify_log.message_id。卡片操作的回调报文
	//   【不带】message_id（2026-09-28 实测留痕为空字段）⇒ 卡片刷新（message/update）
	//   只能靠本列定位卡片（CardRefresher.Refresh 读取）。
	// ★ 写不进去要**可见告警**、绝不静默，但不推翻发送成功判定（消息已发出）：
	//   该行将没有 message_id，后续卡片刷新对此跳过（记 info）。
	switch mid := strings.TrimSpace(out.MessageID); {
	case mid == "":
		s.log.Warn("message/send 已成功但回执无 message_id（该卡片将无法被刷新，请核查回执形态）",
			"biz_no", bizNo, "target", target, "event", event)
	default:
		if err := s.db.UpdateNotifyMessageID(ctx, bizNo, target, event, notifyChannelBot, mid); err != nil {
			s.log.Warn("message_id 写入 t_notify_log 失败（消息已发出，不影响发送成功判定；"+
				"但该行卡片将无法被刷新）",
				"biz_no", bizNo, "target", target, "event", event,
				"message_id", mid, "error", err.Error())
		}
	}
	s.log.Info("待办通知已发送",
		"biz_no", bizNo, "target", target, "event", event,
		"template_id", NotifyTemplateTodo, "feishu_message_id", out.MessageID)
	return nil
}

// feishuUUIDMaxLen 飞书 message/send 的 uuid 官方上限（64 字符）。
const feishuUUIDMaxLen = 64

// uuid 幂等 ID（确定性）：同 (单号, 事件, 收件人) 一小时内飞书侧只发一次 ⇒
// 发送重试天然去重；跨小时重发（如修复循环重试）为新消息，符合预期。
//
// ★ 事件键含 task_id 时（`TASK_ACTIVATED:<task_id>`，FR-M0-17）拼接结果可能超 64 字符
// ⇒ 超限时退化为「前缀 + 64 位 FNV-1a 摘要」（确定性不变，幂等语义不变）。
// ★ 不超限（既有三类事件 TRANSFERRED/ROLLED_BACK/CANCELED）⇒ 与接线前**逐字节相同**，
// 既有行为不受影响（实测成功的 uuid 必然 ≤64，不会落入摘要分支）。
func (s *NotifySender) uuid(bizNo, target, event string) string {
	id := fmt.Sprintf("jx-notify-%s-%s-%s", bizNo, event, target)
	if len(id) <= feishuUUIDMaxLen {
		return id
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id)) // hash.Write 永不返回错误
	return fmt.Sprintf("jx-notify-%016x", h.Sum64())
}

// notifyEventText 事件 → 通知文案（feishu 包不反向依赖 flow，字面量与 flow 事件常量对齐）。
func notifyEventText(event string) string {
	e := strings.ToUpper(strings.TrimSpace(event))
	// 「新待办产生」事件键携带 task_id（`TASK_ACTIVATED:<task_id>`，FR-M0-17）→ 按前缀匹配。
	if strings.HasPrefix(e, "TASK_ACTIVATED") {
		return "您有新的审批待办，请及时处理"
	}
	switch e {
	case "TRANSFERRED": // flow.EventTransferred
		return "您审批的单据已被转交，特此知悉"
	case "ROLLED_BACK": // flow.EventRolledBack
		return "您审批的单据已被回退，特此知悉"
	case "CANCELED": // flow.EventCanceled
		return "您审批的单据已被撤回"
	default:
		return "审批状态有更新，请查看详情"
	}
}

// isNotFound 判定是否 store.ErrNotFound（errors.Is 沿包装链判定）。
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// ---------- 测试 / 开发替身（对齐 fake.go 惯例）----------

// NotifyCall 一次通知发送的留痕（测试断言用）。
type NotifyCall struct {
	BizNo        string
	TargetOpenID string
	Event        string
}

// FakeNotifySender 是 NotifySender 的内存替身（flow.Sender 形态；开发/测试装配用）。
type FakeNotifySender struct {
	// FailFn 覆盖行为（如模拟失败）；nil 时默认成功并记录调用。
	FailFn func(bizNo, target, event string) error

	mu    sync.Mutex
	calls []NotifyCall
}

// NewFakeNotifySender 构造内存替身。
func NewFakeNotifySender() *FakeNotifySender { return &FakeNotifySender{} }

// Send 按 FailFn 行为（默认成功并记录调用）。
func (f *FakeNotifySender) Send(_ context.Context, bizNo, targetOpenID, event string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailFn != nil {
		if err := f.FailFn(bizNo, targetOpenID, event); err != nil {
			return err
		}
	}
	f.calls = append(f.calls, NotifyCall{BizNo: bizNo, TargetOpenID: targetOpenID, Event: event})
	return nil
}

// Count 返回累计发送次数。
func (f *FakeNotifySender) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// Calls 返回全部调用留痕（拷贝）。
func (f *FakeNotifySender) Calls() []NotifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]NotifyCall, len(f.calls))
	copy(out, f.calls)
	return out
}
