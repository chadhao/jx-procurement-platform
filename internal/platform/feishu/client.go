package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
)

// DefaultBaseURL 飞书开放平台域名。
const DefaultBaseURL = "https://open.feishu.cn"

// HTTPClient 是 Client 的真实实现骨架：4 个接口收口 + token 管理。
//
// ★ 联调状态：可编译、可注入、接口签名已定型；因 approval_code（Q1）与飞书凭据尚未到位，
// 具体请求/响应解析未在真机联调。字段映射（控件 id）一律走配置，不在此硬编码。
type HTTPClient struct {
	appID     string
	appSecret string
	baseURL   string
	hc        *http.Client
	tokens    *tokenManager
	log       *slog.Logger
	metrics   *observ.Metrics
}

// NewHTTPClient 构造真实飞书客户端。appID/appSecret 为空时接口调用会失败（缺少凭据）。
func NewHTTPClient(appID, appSecret string, log *slog.Logger, m *observ.Metrics) *HTTPClient {
	base := DefaultBaseURL
	hc := &http.Client{Timeout: 15 * time.Second}
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	if m == nil {
		m = observ.NewMetrics()
	}
	return &HTTPClient{
		appID:     appID,
		appSecret: appSecret,
		baseURL:   base,
		hc:        hc,
		tokens:    &tokenManager{appID: appID, appSecret: appSecret, baseURL: base, hc: hc},
		log:       observ.WithComponent(log, "feishu"),
		metrics:   m,
	}
}

// apiEnvelope 飞书统一响应包裹。
type apiEnvelope struct {
	Code  int             `json:"code"`
	Msg   string          `json:"msg"`
	LogID string          `json:"log_id,omitempty"`
	Data  json.RawMessage `json:"data"`
}

// doJSON 发起带鉴权的 JSON 请求并解析统一包裹；返回 data 段与 log_id。
func (c *HTTPClient) doJSON(ctx context.Context, method, path string, query map[string]string, body io.Reader) (json.RawMessage, string, error) {
	token, err := c.tokens.get(ctx)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, "", err
	}
	if len(query) > 0 {
		q := req.URL.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}

	c.metrics.IncFeishuAPICall()
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("feishu: 调用 %s 失败: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "", fmt.Errorf("feishu: 解析 %s 响应失败: %w", path, err)
	}
	if env.Code != 0 {
		return nil, env.LogID, fmt.Errorf("feishu: %s 返回错误 code=%d msg=%s", path, env.Code, env.Msg)
	}
	return env.Data, env.LogID, nil
}
