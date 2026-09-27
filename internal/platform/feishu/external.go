package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// 三方审批定义适配（external_approvals）——架构转向 ③（04a §13 T01）。
//
// ★ 边界纪律：本包是**唯一**允许触及飞书 HTTP/SDK 的包；上层（internal/approval）
// 只依赖 ExternalApprovalClient 接口，不感知飞书报文。
//
// ★ 只暴露「创建/更新」与「查询」两件事：
//   - `POST /open-apis/approval/v4/external_approvals`：approval_code 命中即更新、未命中即新建；
//   - 查询：按 approval_code 读回定义（对账 / 自检用）。
//   - **不实现删除**：平台无 DELETE 接口（QV2-10），写不出「删定义」的假能力。
//
// ★ 参数结构照 04a §13 T01：名称/分组/可见范围 + external（create_link_pc /
//   create_link_mobile / support_pc / support_mobile / action_callback_url /
//   action_callback_token / action_callback_key）。字段名以设计为准，真机联调按 QV2 实测校准。

// ExternalApprovalDef 三方审批定义（我方 → 飞书）。
type ExternalApprovalDef struct {
	ApprovalCode     string // 为空＝新建；非空＝按此码更新
	Name             string // approval_name
	GroupName        string // group_name
	VisibleScopeJSON string // visible_scope（原样 JSON 片段；空则不下发）
	CreateLinkPC     string // external.create_link_pc（指向我方发起页）
	CreateLinkMobile string // external.create_link_mobile
	SupportPC        bool   // external.support_pc
	SupportMobile    bool   // external.support_mobile
	CallbackURL      string // external.action_callback_url（入站回调端点）
	CallbackToken    string // external.action_callback_token
	CallbackKey      string // external.action_callback_key
}

// ExternalApprovalResult 创建/更新结果（仅在调用成功后返回）。
type ExternalApprovalResult struct {
	ApprovalCode string // 平台回填的 approval_code（缺省取入参）
	FeishuLogID  string // 飞书 log_id（排障用）
}

// ExternalApprovalClient 三方审批定义适配接口。
type ExternalApprovalClient interface {
	// UpsertExternalApproval 建/更新定义（外部返回码决定新建或更新）。
	UpsertExternalApproval(ctx context.Context, def ExternalApprovalDef) (ExternalApprovalResult, error)
	// GetExternalApproval 按 approval_code 读回定义。
	GetExternalApproval(ctx context.Context, approvalCode string) (*ExternalApprovalDef, error)
}

// externalApprovalBody 组装 external_approvals 请求体。
func externalApprovalBody(def ExternalApprovalDef) ([]byte, error) {
	body := map[string]any{
		"approval_name": def.Name,
		"external": map[string]any{
			"create_link_pc":        def.CreateLinkPC,
			"create_link_mobile":    def.CreateLinkMobile,
			"support_pc":            def.SupportPC,
			"support_mobile":        def.SupportMobile,
			"action_callback_url":   def.CallbackURL,
			"action_callback_token": def.CallbackToken,
			"action_callback_key":   def.CallbackKey,
		},
	}
	if strings.TrimSpace(def.ApprovalCode) != "" {
		body["approval_code"] = def.ApprovalCode
	}
	if strings.TrimSpace(def.GroupName) != "" {
		body["group_name"] = def.GroupName
	}
	if strings.TrimSpace(def.VisibleScopeJSON) != "" {
		// 可见范围原样透传（结构由调用方按平台要求提供），避免在此臆造 schema。
		body["visible_scope"] = json.RawMessage(def.VisibleScopeJSON)
	}
	return json.Marshal(body)
}

// UpsertExternalApproval 建/更新三方审批定义。
func (c *HTTPClient) UpsertExternalApproval(ctx context.Context, def ExternalApprovalDef) (ExternalApprovalResult, error) {
	raw, err := externalApprovalBody(def)
	if err != nil {
		return ExternalApprovalResult{}, fmt.Errorf("feishu: 组装三方审批定义失败: %w", err)
	}
	data, logID, err := c.doJSON(ctx, http.MethodPost,
		"/open-apis/approval/v4/external_approvals", nil, bytes.NewReader(raw))
	if err != nil {
		return ExternalApprovalResult{}, err
	}
	var out struct {
		ApprovalCode string `json:"approval_code"`
	}
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out) // 解析失败不致命：以入参码为准
	}
	return ExternalApprovalResult{
		ApprovalCode: firstNonEmpty(out.ApprovalCode, def.ApprovalCode),
		FeishuLogID:  logID,
	}, nil
}

// GetExternalApproval 按 approval_code 读回三方审批定义。
func (c *HTTPClient) GetExternalApproval(ctx context.Context, approvalCode string) (*ExternalApprovalDef, error) {
	if strings.TrimSpace(approvalCode) == "" {
		return nil, fmt.Errorf("feishu: 查询三方审批定义失败: approval_code 为空")
	}
	data, _, err := c.doJSON(ctx, http.MethodGet,
		"/open-apis/approval/v4/external_approvals/"+url.PathEscape(approvalCode), nil, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		ApprovalCode string `json:"approval_code"`
		Name         string `json:"approval_name"`
		GroupName    string `json:"group_name"`
		External     struct {
			CreateLinkPC     string `json:"create_link_pc"`
			CreateLinkMobile string `json:"create_link_mobile"`
			SupportPC        bool   `json:"support_pc"`
			SupportMobile    bool   `json:"support_mobile"`
			CallbackURL      string `json:"action_callback_url"`
			CallbackToken    string `json:"action_callback_token"`
			CallbackKey      string `json:"action_callback_key"`
		} `json:"external"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("feishu: 解析三方审批定义失败: %w", err)
		}
	}
	return &ExternalApprovalDef{
		ApprovalCode:     firstNonEmpty(raw.ApprovalCode, approvalCode),
		Name:             raw.Name,
		GroupName:        raw.GroupName,
		CreateLinkPC:     raw.External.CreateLinkPC,
		CreateLinkMobile: raw.External.CreateLinkMobile,
		SupportPC:        raw.External.SupportPC,
		SupportMobile:    raw.External.SupportMobile,
		CallbackURL:      raw.External.CallbackURL,
		CallbackToken:    raw.External.CallbackToken,
		CallbackKey:      raw.External.CallbackKey,
	}, nil
}

// 编译期断言：HTTPClient 实现 ExternalApprovalClient。
var _ ExternalApprovalClient = (*HTTPClient)(nil)

// ---------- 测试 / 开发替身 ----------

// FakeExternalApprovalClient 是 ExternalApprovalClient 的内存替身。
//
// ★ 按 approval_code 覆盖保存，天然满足「重复注册=更新，不产生第二条定义」，
// 供无凭据的单测与端到端验证使用。可设 UpsertFn 覆盖行为（如模拟失败）。
type FakeExternalApprovalClient struct {
	mu       sync.Mutex
	defs     map[string]ExternalApprovalDef
	upserts  int
	UpsertFn func(ctx context.Context, def ExternalApprovalDef) (ExternalApprovalResult, error)
	GetFn    func(ctx context.Context, approvalCode string) (*ExternalApprovalDef, error)
}

// NewFakeExternalApprovalClient 构造内存替身。
func NewFakeExternalApprovalClient() *FakeExternalApprovalClient {
	return &FakeExternalApprovalClient{defs: map[string]ExternalApprovalDef{}}
}

// UpsertExternalApproval 内存 upsert。
func (f *FakeExternalApprovalClient) UpsertExternalApproval(ctx context.Context, def ExternalApprovalDef) (ExternalApprovalResult, error) {
	if f.UpsertFn != nil {
		return f.UpsertFn(ctx, def)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.defs == nil {
		f.defs = map[string]ExternalApprovalDef{}
	}
	f.defs[def.ApprovalCode] = def
	f.upserts++
	return ExternalApprovalResult{ApprovalCode: def.ApprovalCode, FeishuLogID: "fake-log"}, nil
}

// GetExternalApproval 内存读取。
func (f *FakeExternalApprovalClient) GetExternalApproval(ctx context.Context, approvalCode string) (*ExternalApprovalDef, error) {
	if f.GetFn != nil {
		return f.GetFn(ctx, approvalCode)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	def, ok := f.defs[approvalCode]
	if !ok {
		return nil, fmt.Errorf("feishu: 三方审批定义 %s 不存在", approvalCode)
	}
	cp := def
	return &cp, nil
}

// DefCount 返回内存中定义条数（测试断言「不产生第二条定义」用）。
func (f *FakeExternalApprovalClient) DefCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.defs)
}

// Codes 返回内存中全部 approval_code（升序）。
func (f *FakeExternalApprovalClient) Codes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.defs))
	for k := range f.defs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// UpsertCount 返回 upsert 调用次数。
func (f *FakeExternalApprovalClient) UpsertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upserts
}
