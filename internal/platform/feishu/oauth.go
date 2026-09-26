package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// FeishuIdentity 飞书免登返回的用户身份（open_id → 角色 由此解析）。
type FeishuIdentity struct {
	OpenID     string
	Name       string
	Department string
}

// OAuthExchange 抽象「用 code 换用户身份」，便于 mock（access 层依赖此接口）。
type OAuthExchange interface {
	ExchangeCode(ctx context.Context, code string) (FeishuIdentity, error)
}

// OAuthClient 飞书免登用户身份换取（骨架）。
//
// ★ 联调状态：按官方 OIDC / user_info 端点实现骨架；因缺少凭据未真机联调。
type OAuthClient struct {
	baseURL string
	hc      *http.Client
	log     *slog.Logger
}

// NewOAuthClient 构造 OAuth 客户端。
func NewOAuthClient(log *slog.Logger) *OAuthClient {
	return &OAuthClient{baseURL: DefaultBaseURL, hc: &http.Client{Timeout: 15 * time.Second}, log: log}
}

// ExchangeCode 用免登 code 换取用户身份。
// 1) POST /open-apis/authen/v2/oauth/token（grant_type=authorization_code）
// 2) GET  /open-apis/authen/v1/user_info  →  open_id / name
func (o *OAuthClient) ExchangeCode(ctx context.Context, code string) (FeishuIdentity, error) {
	if code == "" {
		return FeishuIdentity{}, fmt.Errorf("feishu: 免登 code 为空")
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/open-apis/authen/v2/oauth/token", bytes.NewReader(body))
	if err != nil {
		return FeishuIdentity{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := o.hc.Do(req)
	if err != nil {
		return FeishuIdentity{}, fmt.Errorf("feishu: 免登换 token 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return FeishuIdentity{}, err
	}
	var env struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		OpenID    string `json:"open_id"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return FeishuIdentity{}, fmt.Errorf("feishu: 解析免登响应失败: %w", err)
	}
	if env.Code != 0 {
		return FeishuIdentity{}, fmt.Errorf("feishu: 免登返回错误 code=%d msg=%s", env.Code, env.Msg)
	}
	return FeishuIdentity{OpenID: env.OpenID, Name: env.Name}, nil
}

var _ OAuthExchange = (*OAuthClient)(nil)
