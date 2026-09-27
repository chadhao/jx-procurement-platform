package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
)

// message.go —— 审批 Bot 消息「更新」端口（docs/16 §2-F：失败路径的用户反馈 +
// 本批：推进成功后的卡片主动刷新，见 card_refresh.go）。
//
// ★ 背景（官方《三方快捷审批回调》，reference/README.md ★★ 条目）：
//
//	回调处理失败时，飞书卡片退化为「只显示查看详情」（同意/拒绝按钮消失）；飞书审批中心
//	异步继续处理，仍失败 ⇒ 需我方调「更新审批 Bot 消息」接口把卡片标注为失败态。
//
// ★★ 请求体已实测定稿（docs/16 §7 V-2 → 2026-09-28 真机实测，reference/README.md 台账）：
//
//	接口路径＝`POST /open-apis/approval/v1/message/update`（官方 API 清单页
//	《审批任务 · 审批 Bot 消息》）；请求体＝**`{"message_id":"<id>","status":"<status>"}`**：
//	  · 只传 `message_id` ⇒ `60001 "no Status error"`（HTTP 仍 200）；
//	  · 加 `status` ⇒ `{"code":0,"data":{"message_id":"…"},"success"}` ✓；
//	  · `status` 取值与审批状态一致（实测 `"APPROVED"` 成功；本实现按审批终态传
//	    `APPROVED` / `REJECTED`，见 CardRefresher）。
//	取代原「请求体待联调实测 V-2」占位注释（原候选体 {"message_id","content"} 从未实测成功，
//	且按实测缺 status 必 60001 —— 已废止）。

// MessageUpdateClient 审批 Bot 消息更新端口。
//
// 生产实现＝HTTPClient；测试/开发替身＝FakeMessageClient（对齐仓库三分支装配惯例）。
// 上层（CardRefresher 卡片刷新 / bootstrap 修复循环反馈器）只依赖本接口，不感知飞书报文
// （边界纪律同 ContactClient）。
type MessageUpdateClient interface {
	// UpdateApprovalMessage 更新一张审批 Bot 消息卡片（status 与审批状态/终态一致）。
	// messageID 或 status 为空 ⇒ 报错且**不发任何请求**（无卡可更新 / 缺 status 必 60001；
	// docs/16 §2-F 纪律）。
	UpdateApprovalMessage(ctx context.Context, messageID, status string) error
}

// UpdateApprovalMessage 调「更新审批 Bot 消息」（POST /open-apis/approval/v1/message/update）。
//
// ★★ 请求体已实测定稿（2026-09-28 真机，见文件头）：**恰好** `{"message_id":…,"status":…}`
//
//	两键。status 取值与审批状态一致（实测 "APPROVED" 成功）。
//
// ★ message_id 为空 ⇒ 显式报错、**不发同步请求**（无卡可更新；空请求既浪费配额又会拿到
//
//	误导性报错，docs/16 §2-F 纪律「无卡不更新」）。
//
// ★ status 为空 ⇒ 显式报错、**不发同步请求**（实测缺 status ⇒ 60001 "no Status error"，
//
//	HTTP 200 伪装成功；宁可本地可见失败，不发必败请求）。
func (c *HTTPClient) UpdateApprovalMessage(ctx context.Context, messageID, status string) error {
	mid := strings.TrimSpace(messageID)
	if mid == "" {
		return fmt.Errorf("feishu: 更新审批 Bot 消息失败: message_id 为空（无卡可更新，不发请求）")
	}
	st := strings.TrimSpace(status)
	if st == "" {
		return fmt.Errorf("feishu: 更新审批 Bot 消息失败: status 为空" +
			"（实测缺 status ⇒ 60001 no Status error，不发必败请求）")
	}
	// ★ 实测定稿请求体（2026-09-28）：{"message_id":…,"status":…} 恰两键。
	raw, err := json.Marshal(map[string]any{"message_id": mid, "status": st})
	if err != nil {
		return fmt.Errorf("feishu: 组装 message/update 请求体失败: %w", err)
	}
	_, _, err = c.doJSON(ctx, http.MethodPost,
		"/open-apis/approval/v1/message/update", nil, bytes.NewReader(raw))
	return err
}

// 编译期断言：HTTPClient 实现 MessageUpdateClient。
var _ MessageUpdateClient = (*HTTPClient)(nil)

// ---------- 测试 / 开发替身（对齐 fake.go / contact.go 惯例：Fn 钩子 + 调用记录）----------

// messageUpdateCall 一次 UpdateApprovalMessage 调用的留痕（测试断言用）。
type messageUpdateCall struct {
	MessageID string
	Status    string
}

// FakeMessageClient 是 MessageUpdateClient 的内存测试替身。
type FakeMessageClient struct {
	// UpdateFn 覆盖行为（如模拟接口失败）；nil 时默认成功并记录调用。
	UpdateFn func(ctx context.Context, messageID, status string) error

	mu      sync.Mutex
	updates []messageUpdateCall
}

var _ MessageUpdateClient = (*FakeMessageClient)(nil)

// NewFakeMessageClient 构造内存替身。
func NewFakeMessageClient() *FakeMessageClient { return &FakeMessageClient{} }

// UpdateApprovalMessage 按 UpdateFn 行为（默认成功并记录调用）。
func (f *FakeMessageClient) UpdateApprovalMessage(ctx context.Context, messageID, status string) error {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, messageID, status)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, messageUpdateCall{MessageID: messageID, Status: status})
	return nil
}

// UpdateCount 返回累计调用次数（★ 空 message_id 不发请求的断言入口：期望 0）。
func (f *FakeMessageClient) UpdateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates)
}

// Updates 返回全部调用留痕（拷贝；测试断言 message_id/status 用）。
func (f *FakeMessageClient) Updates() []messageUpdateCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]messageUpdateCall, len(f.updates))
	copy(out, f.updates)
	return out
}

// ---------- 修复循环 × 卡片失败反馈（docs/16 §2-F-③ 的装配适配器）----------

// RepairCardFeedback 派生式修复循环「最终失败」行的卡片反馈器。
//
// ★★ 触发点与 CardRefresher（card_refresh.go）**相反**，职责勿混：
//
//   - 本类型＝**修复循环最终失败**路径（第 3 批）：对修复循环重驱动仍失败的行
//     （docs/16 §2-F 情形③）做「处理失败」反馈；
//   - CardRefresher＝**推进成功**路径（本批）：推进成功后把卡片刷成终态。
//     二者触发点不同、语义相反（失败标注 vs 成功刷新），绝不可互相替代。
//
// ★★ 行为变更（2026-09-28 实测后定稿，勿按第 3 批注释回退）：
//
//	message/update 请求体已实测定稿为 {"message_id","status"} —— 第 3 批的
//	{"message_id","content"} 候选体在真机**必 60001**（缺 status，从未成功过）；
//	而「失败态」的 status 取值未实测（APPROVED/REJECTED 是终态，标到**未推进**的单上
//	会伪造审批结果）。按「宁可见失败、不臆造」纪律，本路径**暂缓调用 message/update**，
//	改为 Error 告警：卡片保持两键，用户重复点击会被回调幂等（Duplicate no-op）吸收，
//	处理结论以我方页面 / t_flow_op_log 留痕为准。待实测出「失败态」status 取值后恢复调用。
type RepairCardFeedback struct {
	client MessageUpdateClient
	log    *slog.Logger
}

// NewRepairCardFeedback 构造卡片失败反馈器（client 为 nil 时一切调用为 no-op）。
func NewRepairCardFeedback(client MessageUpdateClient, log *slog.Logger) *RepairCardFeedback {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &RepairCardFeedback{client: client, log: observ.WithComponent(log, "feishu")}
}

// OnRepairFailure 对一行「最终失败」的修复目标做卡片反馈（由 bootstrap 修复循环逐行调用）。
//
// ★ 入参 messageID 即落盘的 `t_flow_op_log.message_id`（0013 新列，recordCallback 写入）。
// ★ 2026-09-28 实测补充：卡片操作的回调报文**不带** message_id（实测留痕为空字段）
// ⇒ 该值恒为空 ⇒ 本路径在真机上本就无卡可标注（与暂缓调用相互印证）。
func (f *RepairCardFeedback) OnRepairFailure(_ context.Context, bizNo, taskID string, round int, messageID string, cause error) {
	if f == nil || f.client == nil {
		return
	}
	// ★ 纪律：message_id 为空 ⇒ 无卡可更新（实测卡片操作回调不带 message_id，常态如此）；
	//   可见告警、不静默（排障入口：t_flow_op_log 留痕 + 修复循环日志）。
	if strings.TrimSpace(messageID) == "" {
		f.log.Warn("审批重驱动最终失败且无 message_id（实测卡片操作回调不带该字段），跳过卡片更新（留痕已在 t_flow_op_log）",
			"biz_no", bizNo, "task_id", taskID, "round", round, "cause", errText(cause))
		return
	}
	// ★★ 失败态标注暂缓（2026-09-28 实测定稿，见类型注释）：不臆造「失败态」status，
	//   不发必败请求；Error 告警保证失败可见（非静默）。
	f.log.Error("审批重驱动最终失败：卡片「失败态」标注暂缓（message/update 已实测 body={message_id,status}，"+
		"失败态 status 取值未实测、不臆造；重复点击由回调幂等吸收，处理结论以我方页面为准）",
		"biz_no", bizNo, "task_id", taskID, "round", round,
		"message_id", messageID, "cause", errText(cause))
}

// errText nil 安全的错误文案。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
