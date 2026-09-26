package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 本文件测试 S3 主存与主备双写。
//
// ★ **本用例能证明什么（以及不能证明什么）—— 如实说明**：
//
//	能证明：① 请求的**协议形状**（method / path / host / 三个签名头 / Authorization 格式）；
//	        ② path-style 与 virtual-host 两种寻址；
//	        ③ 状态码映射（404 → ErrNotFound、5xx → 报错）；
//	        ④ 签名**确定性**（固定时钟 + 固定输入 → 固定签名，可防格式回归）；
//	        ⑤ 主备双写语义与降级次序。
//	**不能证明**：签名会不会被真实 AWS/MinIO/RustFS 接受 —— 那需要真实端点。
//	故另有 `TestS3Integration`：**默认跳过**，设置 `JX_S3_TEST_*` 后即可对真实端点验证。
//	联调前请务必跑它；若签名有偏差（region / path-style / 参与签名的头不对），它会**直接失败**。

func newTestS3(t *testing.T, h http.HandlerFunc) (*S3Store, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	st, err := NewS3(S3Config{
		Endpoint: srv.URL, Bucket: "jx-attach", Region: "us-east-1",
		AccessKey: "AKIDEXAMPLE", SecretKey: "SECRETEXAMPLE", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("构造 S3 失败: %v", err)
	}
	return st, srv
}

// TestS3RequestShape 协议形状：方法 / 路径 / 签名头 / Authorization 格式。
func TestS3RequestShape(t *testing.T) {
	s3Now = func() time.Time { return time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC) }
	defer func() { s3Now = time.Now }()

	var gotPath, gotAuth, gotAmzDate, gotPayloadSHA, gotHost string
	var gotBody []byte
	st, _ := newTestS3(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotAmzDate, gotPayloadSHA, gotHost = r.Header.Get("x-amz-date"), r.Header.Get("x-amz-content-sha256"), r.Host
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	if err := st.Put(context.Background(), "fld_abc", []byte("hello")); err != nil {
		t.Fatalf("PUT 失败: %v", err)
	}
	if gotPath != "/jx-attach/fld_abc" {
		t.Errorf("path-style 路径 = %q, 期望 /jx-attach/fld_abc", gotPath)
	}
	if gotBody == nil || string(gotBody) != "hello" {
		t.Errorf("请求体 = %q", string(gotBody))
	}
	if gotAmzDate != "20260927T010203Z" {
		t.Errorf("x-amz-date = %q, 期望 20260927T010203Z", gotAmzDate)
	}
	if gotPayloadSHA != hashHex([]byte("hello")) {
		t.Errorf("x-amz-content-sha256 = %q, 期望请求体的 SHA-256", gotPayloadSHA)
	}
	if gotHost == "" {
		t.Error("host 头为空")
	}
	// Authorization 头三段式 + Credential 作用域 + SignedHeaders 精确相等 + 签名 64 位十六进制。
	if !strings.HasPrefix(gotAuth, sigAlgorithm+" Credential=AKIDEXAMPLE/20260927/us-east-1/s3/aws4_request") {
		t.Errorf("Authorization Credential 段不符: %q", gotAuth)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("SignedHeaders 不符: %q", gotAuth)
	}
	if m := regexp.MustCompile(`Signature=([0-9a-f]{64})$`).FindStringSubmatch(gotAuth); m == nil {
		t.Errorf("Signature 段不是 64 位小写十六进制: %q", gotAuth)
	}
}

// TestS3SignatureDeterministicAndSensitive 签名确定性 + 对输入敏感（防格式回归）。
func TestS3SignatureDeterministicAndSensitive(t *testing.T) {
	fixed := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	s3Now = func() time.Time { return fixed }
	defer func() { s3Now = time.Now }()

	// ★ 必须复用**同一个 server**：每次新建 server 会拿到不同端口 → host 不同 → 签名必然不同，
	//   那样测的就不是「确定性」而是「端口随机性」（本用例第一版正是踩了这个坑）。
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sig := func(t *testing.T, key string, body []byte, region string) string {
		t.Helper()
		st, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "b", Region: region,
			AccessKey: "AK", SecretKey: "SK", PathStyle: true})
		if err != nil {
			t.Fatalf("构造失败: %v", err)
		}
		if err := st.Put(context.Background(), key, body); err != nil {
			t.Fatalf("PUT 失败: %v", err)
		}
		return auth
	}

	a1 := sig(t, "k1", []byte("x"), "us-east-1")
	a2 := sig(t, "k1", []byte("x"), "us-east-1")
	if a1 != a2 {
		t.Errorf("同输入两次签名不一致（应为确定性）:\n%s\n%s", a1, a2)
	}
	if sig(t, "k2", []byte("x"), "us-east-1") == a1 {
		t.Error("key 变化签名未变（canonical URI 未参与签名？）")
	}
	if sig(t, "k1", []byte("y"), "us-east-1") == a1 {
		t.Error("请求体变化签名未变（payload 哈希未参与签名？）")
	}
	if sig(t, "k1", []byte("x"), "cn-north-1") == a1 {
		t.Error("region 变化签名未变（scope 未参与签名？）")
	}
}
func TestS3StatusCodeMapping(t *testing.T) {
	code := http.StatusNotFound
	st, _ := newTestS3(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code></Error>`))
	})

	if _, err := st.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("404 应映射为 ErrNotFound，实际 %v", err)
	}
	if ok, err := st.Has(context.Background(), "missing"); err != nil || ok {
		t.Errorf("404 的 HEAD 应为 (false, nil)，实际 (%v, %v)", ok, err)
	}

	code = http.StatusInternalServerError
	if _, err := st.Get(context.Background(), "boom"); err == nil {
		t.Error("500 必须报错（不得静默当成功）")
	}
	if err := st.Put(context.Background(), "boom", []byte("x")); err == nil {
		t.Error("PUT 500 必须报错")
	}
}

// TestS3VirtualHostAddressing PathStyle=false 时走 bucket.host 寻址。
//
// ★ 不用 httptest.Server 直连：virtual-host 会生成 `jx-attach.127.0.0.1` 这类 host，
// 本机 DNS 解析不了（真实环境靠 bucket 的 CNAME 记录）。故用**假 Transport 只观察请求**。
func TestS3VirtualHostAddressing(t *testing.T) {
	var gotHost, gotPath string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotHost, gotPath = r.Host, r.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")),
			Header: make(http.Header)}, nil
	})
	st, err := NewS3(S3Config{Endpoint: "https://s3.example.com", Bucket: "jx-attach", Region: "us-east-1",
		AccessKey: "AK", SecretKey: "SK", PathStyle: false, HTTP: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if err := st.Put(context.Background(), "fld_x", []byte("y")); err != nil {
		t.Fatalf("PUT 失败: %v", err)
	}
	if gotHost != "jx-attach.s3.example.com" {
		t.Errorf("virtual-host 模式 host = %q, 期望 jx-attach.s3.example.com", gotHost)
	}
	if gotPath != "/fld_x" {
		t.Errorf("virtual-host 模式路径 = %q, 期望 /fld_x", gotPath)
	}
}
func TestS3RejectsBadConfig(t *testing.T) {
	cases := []S3Config{
		{},
		{Endpoint: "http://x", Bucket: "b"}, // 缺 AK/SK
		{Endpoint: "not-a-url", Bucket: "b", AccessKey: "a", SecretKey: "s"},     // 非法 URL（无 host）
		{Endpoint: "x", Bucket: "b", AccessKey: "a", SecretKey: "s"},             // 缺 scheme
		{Endpoint: "http://x/path", Bucket: "b", AccessKey: "a", SecretKey: "s"}, // endpoint 带路径
	}
	for i, c := range cases {
		if _, err := NewS3(c); err == nil {
			t.Errorf("第 %d 个非法配置未被拒: %+v", i, c)
		}
	}
}

// TestS3RejectsPathTraversalKey key 含上跳必须被拒（防读写目录外对象）。
func TestS3RejectsPathTraversalKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("非法 key 不应发出请求，实际收到 %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	st, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "b", AccessKey: "AK", SecretKey: "SK", PathStyle: true})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	for _, k := range []string{"../etc/passwd", "a/b", `a\b`, "..", ""} {
		if _, err := st.Get(context.Background(), k); err == nil {
			t.Errorf("key %q 未被拒", k)
		}
	}
}

// TestS3Integration 对**真实端点**验证签名（默认跳过）。
//
// 跑法：
//
//	JX_S3_TEST_ENDPOINT=http://127.0.0.1:9000 JX_S3_TEST_BUCKET=clawhark \
//	JX_S3_TEST_AK=xxx JX_S3_TEST_SK=yyy JX_S3_TEST_PATH_STYLE=1 \
//	go test ./internal/objectstore/ -run TestS3Integration -v
func TestS3Integration(t *testing.T) {
	endpoint := os.Getenv("JX_S3_TEST_ENDPOINT")
	bucket := os.Getenv("JX_S3_TEST_BUCKET")
	ak := os.Getenv("JX_S3_TEST_AK")
	sk := os.Getenv("JX_S3_TEST_SK")
	if endpoint == "" || bucket == "" || ak == "" || sk == "" {
		t.Skip("未设置 JX_S3_TEST_*（endpoint/bucket/AK/SK），跳过真实端点验证")
	}
	st, err := NewS3(S3Config{
		Endpoint: endpoint, Bucket: bucket, Region: os.Getenv("JX_S3_TEST_REGION"),
		AccessKey: ak, SecretKey: sk, PathStyle: os.Getenv("JX_S3_TEST_PATH_STYLE") != "0",
	})
	if err != nil {
		t.Fatalf("构造 S3 失败: %v", err)
	}
	ctx := context.Background()
	key := "jx-integration-probe.txt"
	if err := st.Put(ctx, key, []byte("probe-2026-09-27")); err != nil {
		t.Fatalf("真实端点 PUT 失败（签名或配置有偏差）: %v", err)
	}
	got, err := st.Get(ctx, key)
	if err != nil {
		t.Fatalf("真实端点 GET 失败: %v", err)
	}
	if string(got) != "probe-2026-09-27" {
		t.Fatalf("回读内容不符: %q", string(got))
	}
	if ok, err := st.Has(ctx, key); err != nil || !ok {
		t.Fatalf("真实端点 HEAD 失败: (%v, %v)", ok, err)
	}
}

// roundTripFunc 把函数适配成 http.RoundTripper（供只想观察请求、不想真正发网络的用例）。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
