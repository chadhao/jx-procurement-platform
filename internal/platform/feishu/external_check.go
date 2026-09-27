package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// external_check.go —— 出方向对账端口（架构转向 ③ · T03；04a §9.2）。
//
// 职责：调飞书 `POST /open-apis/approval/v4/external_instances/check`，取回**差异实例**
// （平台口径：`check` 只返回 `diff_instances`，**不返回全量**）。
//
// ★ 本包只上抛"平台观测到的差异"，**不在此判方向、不做补拉**：
// 方向判断与"是否重推"由上层 `internal/sync.ApprovalReconciler` 负责
// （R06：新对账器**独立于 ingest**、**不复用**旧 `Reconciler`；禁止"缺则补"的覆盖路径）。
//
// ★ 边界纪律：飞书 HTTP 只在本包发生；上层只依赖 `ExtCheckClient` 接口
//   （与 `ExternalApprovalClient` / `PushClient` 同式的独立小接口，不改动 `Client`）。
// ★ 联调状态：字段名以设计（04a §9.2）为准，真机联调按 QV2/Q17 校准（同 external.go）。

// ExternalInstanceState 平台侧观测到的某实例状态（供上层做**方向判断**）。
type ExternalInstanceState struct {
	// InstanceID 平台侧实例标识（＝我方推送的 `instance_id` ＝ `InstanceCode`）。
	InstanceID string
	// UpdateTime 平台侧观测到的版本号（`.update_time`）。★ 缺失/不可判时为 0。
	UpdateTime int64
	// Status 平台侧观测到的状态（`.status`）。缺失时为空串。
	Status string
}

// ExtCheckClient 出方向对账端口：按 approval_code 取回需要关注的**差异实例**。
type ExtCheckClient interface {
	// CheckExternalInstances 取回某 approval_code 下平台观测到的差异实例。
	CheckExternalInstances(ctx context.Context, approvalCode string) ([]ExternalInstanceState, error)
}

// CheckExternalInstances 调 external_instances/check 取差异实例。
func (c *HTTPClient) CheckExternalInstances(ctx context.Context, approvalCode string) ([]ExternalInstanceState, error) {
	if strings.TrimSpace(approvalCode) == "" {
		return nil, fmt.Errorf("feishu: 对账检查失败: approval_code 为空")
	}
	body, err := json.Marshal(map[string]any{"approval_code": approvalCode})
	if err != nil {
		return nil, fmt.Errorf("feishu: 组装 external_instances/check 失败: %w", err)
	}
	data, _, err := c.doJSON(ctx, http.MethodPost,
		"/open-apis/approval/v4/external_instances/check", nil, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var raw struct {
		DiffInstances []struct {
			InstanceID string `json:"instance_id"`
			UpdateTime int64  `json:"update_time"`
			Status     string `json:"status"`
		} `json:"diff_instances"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("feishu: 解析 external_instances/check 失败: %w", err)
		}
	}
	out := make([]ExternalInstanceState, 0, len(raw.DiffInstances))
	for _, d := range raw.DiffInstances {
		if strings.TrimSpace(d.InstanceID) == "" {
			continue
		}
		out = append(out, ExternalInstanceState{
			InstanceID: d.InstanceID, UpdateTime: d.UpdateTime, Status: d.Status,
		})
	}
	return out, nil
}

// 编译期断言：HTTPClient 实现 ExtCheckClient。
var _ ExtCheckClient = (*HTTPClient)(nil)

// ---------- 测试 / 开发替身 ----------

// FakeExtCheckClient 是 ExtCheckClient 的内存替身（按 approval_code 覆盖保存差异实例）。
type FakeExtCheckClient struct {
	mu     sync.Mutex
	states map[string][]ExternalInstanceState
	// Err 非 nil 时，所有调用返回该错误（模拟平台不可用）。
	Err error
}

// NewFakeExtCheckClient 构造内存替身。
func NewFakeExtCheckClient() *FakeExtCheckClient {
	return &FakeExtCheckClient{states: map[string][]ExternalInstanceState{}}
}

// Set 设置某 approval_code 的差异实例（测试/开发用）。
func (f *FakeExtCheckClient) Set(approvalCode string, sts ...ExternalInstanceState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[string][]ExternalInstanceState{}
	}
	f.states[approvalCode] = sts
}

// CheckExternalInstances 内存读取。
func (f *FakeExtCheckClient) CheckExternalInstances(_ context.Context, approvalCode string) ([]ExternalInstanceState, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[approvalCode], nil
}
