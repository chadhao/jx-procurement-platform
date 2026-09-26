package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// S3Store S3 兼容对象存储（**主存**，架构 §7.5 / ADR-08）。
//
// ★ 为什么自己签 SigV4 而不是引 SDK：本项目纪律「**新增依赖需先经用户同意**」，
// 而 SigV4 用标准库（`crypto/hmac` + `crypto/sha256`）即可完整实现 ——
// 故选择**手写签名、零新增依赖**。同时该实现天然兼容 S3 协议族（AWS S3 / MinIO / **RustFS**）。
//
// ★ 作用域：本实现只需 GET / PUT / HEAD 三个动作（附件**只做下载** + 缓存，不做上传业务）。
//
// ⚠️ **可信度声明（必须如实告知）**：签名的**协议形状**（CanonicalRequest / StringToSign /
// Authorization 头）已由用例逐项断言，但**签名的密码学校验只能在真实端点完成** ——
// 离线环境无法证明"这个签名会被 AWS/MinIO 接受"。故另提供**默认跳过**的集成测试：
//
//	JX_S3_TEST_ENDPOINT=... JX_S3_TEST_BUCKET=... JX_S3_TEST_AK=... JX_S3_TEST_SK=... \
//	go test ./internal/objectstore/ -run TestS3Integration -v
//
// 联调时请先跑该用例；若签名有偏差（例如 region 或 path-style 不对），它会**直接失败**而不是静默。
type S3Store struct {
	cfg  S3Config
	base *url.URL
}

// S3Config S3 连接配置（全部来自环境变量，不入库、不进版本库）。
type S3Config struct {
	Endpoint  string // 如 http://127.0.0.1:9000 或 https://s3.example.com
	Bucket    string // 桶名
	Region    string // 缺省 us-east-1（MinIO / RustFS 通常用缺省）
	AccessKey string // AK
	SecretKey string // SK
	PathStyle bool   // true＝http://host/bucket/key（MinIO/RustFS 常用）；false＝http://bucket.host/key
	HTTP      *http.Client
}

var _ Store = (*S3Store)(nil)

// NewS3 构造 S3 存储；配置不完整时返回 error（宁可启动即失败，也不要运行时静默降级）。
func NewS3(cfg S3Config) (*S3Store, error) {
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("objectstore: S3 配置不完整（endpoint / bucket / AK / SK 均必填）")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("objectstore: 解析 S3 endpoint 失败: %w", err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("objectstore: S3 endpoint 必须是 http(s)://host[:port]，实际 %q", cfg.Endpoint)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("objectstore: S3 endpoint 不应带路径（如需路径前缀请用 path-style bucket），实际 %q", cfg.Endpoint)
	}
	if strings.TrimSpace(cfg.Region) == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	return &S3Store{cfg: cfg, base: u}, nil
}

// Kind 后端类型。
func (s *S3Store) Kind() string { return "s3" }

// Put 写入对象（PUT；同 key 覆盖，天然幂等）。
func (s *S3Store) Put(ctx context.Context, key string, data []byte) error {
	req, err := s.newRequest(ctx, http.MethodPut, key, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	s.sign(req, hashHex(data), s3Now())
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("objectstore: S3 PUT 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("objectstore: S3 PUT %s 返回 %d: %s", key, resp.StatusCode, snippet(resp.Body))
	}
	return nil
}

// Get 读取对象；404 → ErrNotFound。
func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	req, err := s.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	s.sign(req, emptyPayloadSHA256, s3Now())
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("objectstore: S3 GET 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("objectstore: S3 GET %s 返回 %d: %s", key, resp.StatusCode, snippet(resp.Body))
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("objectstore: 读取 S3 响应体失败: %w", err)
	}
	return b, nil
}

// Has 判断对象是否存在（HEAD；404 → false）。
func (s *S3Store) Has(ctx context.Context, key string) (bool, error) {
	req, err := s.newRequest(ctx, http.MethodHead, key, nil)
	if err != nil {
		return false, err
	}
	s.sign(req, emptyPayloadSHA256, s3Now())
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("objectstore: S3 HEAD 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return false, fmt.Errorf("objectstore: S3 HEAD %s 返回 %d", key, resp.StatusCode)
	}
	return true, nil
}

// newRequest 组装请求（含 path-style / virtual-host 两种寻址）。
func (s *S3Store) newRequest(ctx context.Context, method, key string, body io.Reader) (*http.Request, error) {
	k, err := sanitizeKey(key)
	if err != nil {
		return nil, err
	}
	u := *s.base
	if s.cfg.PathStyle {
		u.Path = "/" + s.cfg.Bucket + "/" + k
	} else {
		u.Host = s.cfg.Bucket + "." + u.Host
		u.Path = "/" + k
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	return req, nil
}

// emptyPayloadSHA256 ＝ sha256("")（GET/HEAD 的 payload 哈希）。
const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

const sigAlgorithm = "AWS4-HMAC-SHA256"

// s3Now 取当前时间（**仅供测试注入固定时钟**，使签名可确定性断言）。
// 生产路径即 time.Now，不改变任何行为。
var s3Now = time.Now

// sign 为请求附加 AWS Signature V4（header 方式）。
//
// 签名只覆盖 `host` / `x-amz-content-sha256` / `x-amz-date` 三个头（最小集，够用且不易错）。
// 任何新增的参与签名的头都必须同时进入 canonicalHeaders 与 signedHeaders，否则签名必然失败。
func (s *S3Store) sign(req *http.Request, payloadSHA string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadSHA)

	canonicalHeaders := "host:" + req.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadSHA + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.Query().Encode(), // Go 的 Encode() 已按 key 升序且百分号编码，符合规范
		canonicalHeaders,
		signedHeaders,
		payloadSHA,
	}, "\n")

	scope := dateStamp + "/" + s.cfg.Region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		sigAlgorithm, amzDate, scope, hashHex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), dateStamp)
	kRegion := hmacSHA256(kDate, s.cfg.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", sigAlgorithm+
		" Credential="+s.cfg.AccessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+signature)
}

// hashHex 十六进制小写 SHA-256。
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hmacSHA256 以 key 为密钥对 data 做 HMAC-SHA256。
func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// snippet 读取响应体前若干字节用于报错（不把整段错误页塞进日志/响应）。
func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}
