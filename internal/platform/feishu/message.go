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

// message.go —— 审批 Bot 消息「更新」端口（docs/16 §2-F：失败路径的用户反馈）。
//
// ★ 背景（官方《三方快捷审批回调》，reference/README.md ★★ 条目）：
//
//	回调处理失败时，飞书卡片退化为「只显示查看详情」（同意/拒绝按钮消失）；飞书审批中心
//	异步继续处理，仍失败 ⇒ 需我方调「更新审批 Bot 消息」接口把卡片标注为失败态。
//
// ★★ 实测状态（docs/16 §7 V-2，务必知悉）：
//
//	接口**路径已核**＝`POST /open-apis/approval/v1/message/update`（官方 API 清单页
//	《审批任务 · 审批 Bot 消息》）；但**请求体字段尚未实测** —— 本文件对请求体**零硬编码**：
//	签名收 `body map[string]any` 由调用方组装，并在注释中显式标注「待实测」。
//	联调按响应报错逐字段校准后定稿（docs/16 §2-F-③ / §7 V-2），**不得当结论使用**。

// MessageUpdateClient 审批 Bot 消息更新端口。
//
// 生产实现＝HTTPClient；测试/开发替身＝FakeMessageClient（对齐仓库三分支装配惯例）。
// 上层（bootstrap 修复循环反馈器）只依赖本接口，不感知飞书报文（边界纪律同 ContactClient）。
type MessageUpdateClient interface {
	// UpdateApprovalMessage 更新一张审批 Bot 消息卡片。
	// messageID 为空 ⇒ 报错且**不发任何请求**（无卡可更新；docs/16 §2-F 纪律）。
	UpdateApprovalMessage(ctx context.Context, messageID string, body map[string]any) error
}

// UpdateApprovalMessage 调「更新审批 Bot 消息」（POST /open-apis/approval/v1/message/update）。
//
// ★★ **路径已核、请求体字段待联调实测（docs/16 §7 V-2）**：本函数对 body **不做任何字段
//
//	假设与校验**，原样序列化透传 —— 请求体由调用方组装（见 RepairCardFeedback 的候选体
//	注释）；联调实测后如有出入，以实测为准并回写调用方与 docs/16。
//
// ★ message_id 为空 ⇒ 显式报错、**不发同步请求**（无卡可更新；空请求既浪费配额又会拿到
//
//	误导性报错，docs/16 §2-F 纪律「无卡不更新」）。
func (c *HTTPClient) UpdateApprovalMessage(ctx context.Context, messageID string, body map[string]any) error {
	mid := strings.TrimSpace(messageID)
	if mid == "" {
		return fmt.Errorf("feishu: 更新审批 Bot 消息失败: message_id 为空（无卡可更新，不发请求）")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("feishu: 组装 message/update 请求体失败: %w", err)
	}
	// ★ 路径已核（官方 API 清单页《审批任务 · 审批 Bot 消息》）；请求体字段待实测（V-2）。
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
	Body      map[string]any
}

// FakeMessageClient 是 MessageUpdateClient 的内存测试替身。
type FakeMessageClient struct {
	// UpdateFn 覆盖行为（如模拟接口失败）；nil 时默认成功并记录调用。
	UpdateFn func(ctx context.Context, messageID string, body map[string]any) error

	mu      sync.Mutex
	updates []messageUpdateCall
}

var _ MessageUpdateClient = (*FakeMessageClient)(nil)

// NewFakeMessageClient 构造内存替身。
func NewFakeMessageClient() *FakeMessageClient { return &FakeMessageClient{} }

// UpdateApprovalMessage 按 UpdateFn 行为（默认成功并记录调用）。
func (f *FakeMessageClient) UpdateApprovalMessage(ctx context.Context, messageID string, body map[string]any) error {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, messageID, body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, messageUpdateCall{MessageID: messageID, Body: body})
	return nil
}

// UpdateCount 返回累计调用次数（★ 空message_id 不发请求的断言入口：期望 0）。
func (f *FakeMessageClient) UpdateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates)
}

// Updates 返回全部调用留痕（拷贝；测试断言请求体用）。
func (f *FakeMessageClient) Updates() []messageUpdateCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]messageUpdateCall, len(f.updates))
	copy(out, f.updates)
	return out
}

// ---------- 修复循环 × 卡片失败反馈（docs/16 §2-F-③ 的装配适配器）----------

// RepairCardFeedback 派生式修复循环「最终失败」行的卡片反馈器：
//
//	对修复循环重驱动仍失败的行（docs/16 §2-F 情形③），用**已落盘**的
//	`t_flow_op_log.message_id` 调 message/update，把卡片标注为「处理失败，请到我方页面重试」。
//
// ★ 纪律（docs/16 §2-F，逐条执行）：
//   - **message_id 为空 ⇒ 不发同步请求**（无卡可更新；情形④ 未受理 4xx 本就无 message_id，
//     属可接受降级：已有审计留痕，不调 message/update）；
//   - **失败只记日志、不回滚业务**（与「落盘即 200」#69 ① 纪律一致；下一轮修复循环重试标注）；
//   - **进程内防重**：同一 (biz_no, task_id, round) 只成功标注一次 —— 修复循环每 30s 扫一轮，
//     不防重会对不可恢复失败每 30s 重发一次卡片更新（配额浪费 + 用户端卡片反复闪动）。
//     进程重启后 seen 集清空 ⇒ 至多重标一次，卡片更新本身幂等、无数据损坏面。
type RepairCardFeedback struct {
	client MessageUpdateClient
	log    *slog.Logger

	mu   sync.Mutex
	done map[string]bool // 已成功标注的 key（biz_no/task_id/round）
}

// NewRepairCardFeedback 构造卡片失败反馈器（client 为 nil 时一切调用为 no-op）。
func NewRepairCardFeedback(client MessageUpdateClient, log *slog.Logger) *RepairCardFeedback {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &RepairCardFeedback{client: client, log: observ.WithComponent(log, "feishu"), done: map[string]bool{}}
}

// OnRepairFailure 对一行「最终失败」的修复目标做卡片反馈（由 bootstrap 修复循环逐行调用）。
//
// ★ 入参 messageID 即落盘的 `t_flow_op_log.message_id`（0013 新列，recordCallback 写入）。
func (f *RepairCardFeedback) OnRepairFailure(ctx context.Context, bizNo, taskID string, round int, messageID string, cause error) {
	if f == nil || f.client == nil {
		return
	}
	// ★ 纪律一：message_id 为空 ⇒ 无卡可更新，**不发同步请求**（可见告警，不静默）。
	if strings.TrimSpace(messageID) == "" {
		f.log.Warn("审批重驱动最终失败且无 message_id（非卡片操作报文），跳过卡片更新（留痕已在 t_flow_op_log）",
			"biz_no", bizNo, "task_id", taskID, "round", round, "cause", errText(cause))
		return
	}
	// ★ 纪律三：进程内防重（同 key 只成功标注一次）。
	key := fmt.Sprintf("%s/%s/%d", bizNo, taskID, round)
	f.mu.Lock()
	if f.done[key] {
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()

	// ★★ 候选请求体 —— **docs/16 §7 V-2：message/update 请求体字段未实测**。
	//	路径已核（官方 API 清单页《审批任务 · 审批 Bot 消息》）；字段结构此处仅给**最小候选**
	//	（定位到卡片 + 一句失败文案），联调按响应报错逐字段校准后定稿，**不得当结论使用**。
	body := map[string]any{
		"message_id": messageID,
		"content":    fmt.Sprintf("审批处理失败，请到我方页面重试（单号 %s）", bizNo),
	}
	if err := f.client.UpdateApprovalMessage(ctx, messageID, body); err != nil {
		// ★ 纪律二：失败只记日志、不回滚业务（不标记 done ⇒ 下一轮修复循环重试标注）。
		f.log.Error("审批重驱动最终失败且卡片更新失败（只记日志，不回滚业务；下一轮重试标注）",
			"biz_no", bizNo, "task_id", taskID, "round", round,
			"message_id", messageID, "cause", errText(cause), "update_error", err.Error())
		return
	}
	f.mu.Lock()
	f.done[key] = true
	f.mu.Unlock()
	f.log.Info("审批重驱动最终失败，已调用 message/update 标注卡片（字段结构待 V-2 实测定稿）",
		"biz_no", bizNo, "task_id", taskID, "round", round, "message_id", messageID)
}

// errText nil 安全的错误文案。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
