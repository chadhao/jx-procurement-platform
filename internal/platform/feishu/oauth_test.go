package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExchangeCodeSendsOfficialRequiredBody 断言实际发出的 token 请求体含官方必填字段：
// grant_type / client_id / client_secret / code（官方《获取 user_access_token（v2）》），
// 以及 access_token → user_info → open_id 的完整链路。
func TestExchangeCodeSendsOfficialRequiredBody(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/authen/v2/oauth/token":
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &gotBody); err != nil {
				t.Errorf("token 请求体非法: %v", err)
			}
			_, _ = w.Write([]byte(`{"access_token":"uat-1","expires_in":7200,"token_type":"Bearer"}`))
		case "/open-apis/authen/v1/user_info":
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"code":0,"data":{"open_id":"ou_x","name":"张三"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	o := NewOAuthClient("cli_app", "sec_app", "https://jx.example.com/auth/feishu/callback", nil)
	o.baseURL = ts.URL // 测试专用：指向假端点

	ident, err := o.ExchangeCode(context.Background(), "code-1")
	if err != nil {
		t.Fatalf("ExchangeCode 失败: %v", err)
	}
	if ident.OpenID != "ou_x" || ident.Name != "张三" {
		t.Errorf("身份 = %+v, 期望 open_id=ou_x name=张三", ident)
	}
	if gotAuth != "Bearer uat-1" {
		t.Errorf("user_info Authorization = %q, 期望 %q", gotAuth, "Bearer uat-1")
	}

	// 官方必填四项 + 与授权时一致的 redirect_uri。
	for k, want := range map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     "cli_app",
		"client_secret": "sec_app",
		"code":          "code-1",
		"redirect_uri":  "https://jx.example.com/auth/feishu/callback",
	} {
		if gotBody[k] != want {
			t.Errorf("token 请求体 %s = %v, 期望 %v", k, gotBody[k], want)
		}
	}
}

// TestExchangeCodeFailsOnNonZeroCode 断言：响应 code != 0 / error 非空 ⇒ 判失败
// 并带 error_description（HTTP 200 不代表成功，勿把失败当成功）。
func TestExchangeCodeFailsOnNonZeroCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":99991672,"error":"invalid_grant","error_description":"code 已使用或已过期"}`))
	}))
	defer ts.Close()

	o := NewOAuthClient("cli_app", "sec_app", "", nil)
	o.baseURL = ts.URL

	_, err := o.ExchangeCode(context.Background(), "code-bad")
	if err == nil {
		t.Fatal("code != 0 应判失败，实际 nil")
	}
	for _, want := range []string{"99991672", "invalid_grant", "code 已使用或已过期"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺少 %q: %v", want, err)
		}
	}
}

// TestExchangeCodeRequiresCredentials 缺凭据 ⇒ 可见报错（不静默发出必失败的请求）。
func TestExchangeCodeRequiresCredentials(t *testing.T) {
	o := NewOAuthClient("", "", "", nil)
	if _, err := o.ExchangeCode(context.Background(), "code-1"); err == nil {
		t.Fatal("缺 client_id/secret 应报错")
	}
}
