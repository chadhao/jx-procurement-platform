package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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

// OAuthClient 飞书免登用户身份换取。
//
// 官方契约（本文件按官方原文实现，来源见各方法注释）：
//  1. POST /open-apis/authen/v2/oauth/token —— 《获取 user_access_token（v2）》
//     请求体必填 `grant_type` / `client_id` / `client_secret` / `code`；
//     响应 `access_token` 即 user_access_token（另有 expires_in / refresh_token / token_type）。
//     ★ open_id **不在** token 响应里，须另调 user_info。
//  2. GET /open-apis/authen/v1/user_info —— `Authorization: Bearer <user_access_token>`
//     响应 `data.open_id` / `data.name`。
//
// ★ TODO（本批不做）：官方已将 v2 标记为**历史版本**并推荐 v3
// （POST /open-apis/authen/v3/oauth/token），后续可平迁，方法签名不变。
type OAuthClient struct {
	baseURL     string
	appID       string // JX_APP_ID（client_id）
	appSecret   string // JX_APP_SECRET（client_secret）
	redirectURI string // 免登回调地址（token 请求体官方非必填；与授权时一致带上）
	hc          *http.Client
	log         *slog.Logger
}

// NewOAuthClient 构造 OAuth 客户端。
//
// ★ 2026-09 补全：请求体此前只有 grant_type+code，缺 client_id / client_secret，
// 按官方必填口径**必然失败**；现从配置（Env）注入，绝不硬编码。
func NewOAuthClient(appID, appSecret, redirectURI string, log *slog.Logger) *OAuthClient {
	return &OAuthClient{
		baseURL:     DefaultBaseURL,
		appID:       strings.TrimSpace(appID),
		appSecret:   strings.TrimSpace(appSecret),
		redirectURI: strings.TrimSpace(redirectURI),
		hc:          &http.Client{Timeout: 15 * time.Second},
		log:         log,
	}
}

// ExchangeCode 用免登 code 换取用户身份。
// 1) POST /open-apis/authen/v2/oauth/token（grant_type=authorization_code）
// 2) GET  /open-apis/authen/v1/user_info  →  open_id / name
func (o *OAuthClient) ExchangeCode(ctx context.Context, code string) (FeishuIdentity, error) {
	if strings.TrimSpace(code) == "" {
		return FeishuIdentity{}, fmt.Errorf("feishu: 免登 code 为空")
	}
	if o.appID == "" || o.appSecret == "" {
		return FeishuIdentity{}, fmt.Errorf("feishu: 未配置 JX_APP_ID / JX_APP_SECRET，无法用 code 换取身份")
	}

	token, err := o.exchangeToken(ctx, code)
	if err != nil {
		return FeishuIdentity{}, err
	}
	return o.fetchUserInfo(ctx, token)
}

// exchangeToken 用 code 换 user_access_token（官方《获取 user_access_token（v2）》）。
//
// ★ 判定纪律：HTTP 200 **不代表成功** —— 响应 `code != 0` 或 `error` 非空一律判失败，
// 并带上 `error_description`（不静默把失败当成功）。
func (o *OAuthClient) exchangeToken(ctx context.Context, code string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     o.appID,
		"client_secret": o.appSecret,
		"code":          code,
		"redirect_uri":  o.redirectURI, // 官方非必填；与授权时一致带上
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/open-apis/authen/v2/oauth/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := o.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu: 免登换 token 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var tok struct {
		Code             int    `json:"code"`
		Msg              string `json:"msg"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		ExpiresIn        int64  `json:"expires_in"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return "", fmt.Errorf("feishu: 解析免登 token 响应失败: %w", err)
	}
	if tok.Code != 0 || tok.Error != "" {
		detail := tok.ErrorDescription
		if detail == "" {
			detail = tok.Msg
		}
		return "", fmt.Errorf("feishu: 免登换 token 被拒 code=%d error=%s detail=%s",
			tok.Code, tok.Error, detail)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return "", fmt.Errorf("feishu: 免登 token 响应缺少 access_token（原响应 %d 字节）", len(raw))
	}
	return tok.AccessToken, nil
}

// fetchUserInfo 用 user_access_token 取 open_id / name（官方 /authen/v1/user_info）。
func (o *OAuthClient) fetchUserInfo(ctx context.Context, accessToken string) (FeishuIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		o.baseURL+"/open-apis/authen/v1/user_info", nil)
	if err != nil {
		return FeishuIdentity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := o.hc.Do(req)
	if err != nil {
		return FeishuIdentity{}, fmt.Errorf("feishu: 免登取用户信息失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return FeishuIdentity{}, err
	}
	var ui struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			OpenID string `json:"open_id"`
			Name   string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ui); err != nil {
		return FeishuIdentity{}, fmt.Errorf("feishu: 解析用户信息响应失败: %w", err)
	}
	if ui.Code != 0 {
		return FeishuIdentity{}, fmt.Errorf("feishu: 用户信息返回错误 code=%d msg=%s", ui.Code, ui.Msg)
	}
	if strings.TrimSpace(ui.Data.OpenID) == "" {
		return FeishuIdentity{}, fmt.Errorf("feishu: 用户信息响应缺少 open_id")
	}
	return FeishuIdentity{OpenID: ui.Data.OpenID, Name: ui.Data.Name}, nil
}

var _ OAuthExchange = (*OAuthClient)(nil)
