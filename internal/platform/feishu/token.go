package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// tokenManager 管理 tenant_access_token：单实例获取、提前刷新（FR-M0-01）。
// 注意：token 端点属鉴权基础设施，不计入「4 个业务接口」。
type tokenManager struct {
	appID     string
	appSecret string
	baseURL   string
	hc        *http.Client

	mu       sync.Mutex
	token    string
	expireAt time.Time
}

const refreshAhead = 8 * time.Minute // 提前 8 分钟刷新（建议 5–10 分钟）

// get 返回有效 token；缓存过期前提前刷新，并发调用互相不顶掉（单实例串行化）。
func (t *tokenManager) get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && time.Now().Add(refreshAhead).Before(t.expireAt) {
		return t.token, nil
	}

	body, _ := json.Marshal(map[string]string{
		"app_id":     t.appID,
		"app_secret": t.appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := t.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu: 获取 tenant_access_token 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var env struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("feishu: 解析 token 响应失败: %w", err)
	}
	if env.Code != 0 || env.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu: 获取 token 返回错误 code=%d msg=%s", env.Code, env.Msg)
	}
	t.token = env.TenantAccessToken
	t.expireAt = time.Now().Add(time.Duration(env.Expire) * time.Second)
	return t.token, nil
}
