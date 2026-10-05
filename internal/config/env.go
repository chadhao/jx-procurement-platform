// Package config 负责配置加载：环境变量（密钥）优先 + 配置表（映射/规则）装载。
// 纪律：密钥一律来自环境变量，不入库、不进版本库（FR-M8-06）。
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Env 承载全部运行期环境变量（对应架构 §6.4 清单）。
type Env struct {
	AppID     string // JX_APP_ID（敏感）
	AppSecret string // JX_APP_SECRET（敏感）
	DataDir   string // JX_DATA_DIR
	// AttachDir 附件对象存储目录（JX_ATTACH_DIR）。
	//
	// ★ 现状（B39）：先提供**本地落盘**作为可运行的最小实现；生产口径是**云侧 S3 主存 +
	// RustFS 异地备份**（架构 §7.5 / ADR-08），待接入 S3 后只需替换 objectstore 实现。
	// 置空＝**不缓存、直接转发**（降级可用，不是静默丢功能）。
	AttachDir     string // JX_ATTACH_DIR
	DBPath        string // JX_DB_PATH
	ListenAddr    string // JX_LISTEN_ADDR
	SessionKey    string // JX_SESSION_KEY（敏感）
	InternalToken string // JX_INTERNAL_TOKEN（敏感）
	LockPath      string // JX_LOCK_PATH

	S3Endpoint string // JX_S3_ENDPOINT
	S3Bucket   string // JX_S3_BUCKET
	S3Region   string // JX_S3_REGION（缺省 us-east-1）
	// S3PathStyle 寻址方式：true＝http://host/bucket/key（MinIO / RustFS 常用）；false＝http://bucket.host/key（云 S3）。
	S3PathStyle bool   // JX_S3_PATH_STYLE（缺省 true）
	S3AK        string // JX_S3_AK（敏感）
	S3SK        string // JX_S3_SK（敏感）

	RustFSEndpoint string // JX_RUSTFS_ENDPOINT
	// RustFSBucket 缺省取 JX_S3_BUCKET；RustFSRegion 缺省取 JX_S3_REGION。
	RustFSBucket    string // JX_RUSTFS_BUCKET
	RustFSRegion    string // JX_RUSTFS_REGION
	RustFSPathStyle bool   // JX_RUSTFS_PATH_STYLE（缺省取 JX_S3_PATH_STYLE）
	RustFSAK        string // JX_RUSTFS_AK（敏感）
	RustFSSK        string // JX_RUSTFS_SK（敏感）

	// CallbackDomain 对外回调域名（JX_CALLBACK_DOMAIN，docs/04 §6.4 配置键正本）。
	// ★ 消费端：`POST /api/admin/approval/defs/sync` 组装定义的 `action_callback_url`
	//   （＝ `<domain>/approval/external/callback`）。
	CallbackDomain string // JX_CALLBACK_DOMAIN
	// OAuthRedirectURI 飞书免登（授权登录）回调地址（JX_OAUTH_REDIRECT_URI，可选）。
	// ★ 消费端：`GET /api/auth/authorize-url` 组装官方授权页 URL 的 `redirect_uri` 参数，
	//   且**必须与飞书开放平台【安全设置】的重定向 URL 白名单逐字一致**（含 scheme/域名/路径），
	//   不得包含 `#`（官方：fragment 会被拼到回调末尾，SPA 取不到 code）。
	// 未配置时缺省取 `JX_CALLBACK_DOMAIN + "/auth/feishu/callback"`（见 FeishuRedirectURI）。
	OAuthRedirectURI string // JX_OAUTH_REDIRECT_URI
	// ActionCallbackToken 三方审批定义下发的**回调校验 token**（敏感，docs/04 §6.4）。
	// ★ 一处配置、两侧一致：注册定义时随 `DefInput.CallbackToken` 下发飞书
	//   （external.action_callback_token），本地落 `t_approval_def.callback_token` 同值
	//   （docs/16 §2-C）。未配置 ⇒ sync 端点**可见拒绝**（定义无 token 则回调校验恒失败）。
	ActionCallbackToken string // JX_ACTION_CALLBACK_TOKEN（敏感）

	RunEnv            string // JX_ENV: prod / test
	DevMode           bool   // DEV_MODE
	ReconcileInterval time.Duration
	// ApprovalReconcileInterval 审批对账周期（JX_APPROVAL_RECONCILE_INTERVAL，
	// Go duration 串如 "5m"，默认 5m —— 01a §5.5 约束2「对账频率可配置 + 自适应」；
	// ★ 与上面旧 ReconcileInterval（JX_RECONCILE_INTERVAL_HOURS，旧器）无关）。
	ApprovalReconcileInterval time.Duration
	// FeishuMonthlyQuota 飞书 API 月配额基数（JX_FEISHU_MONTHLY_QUOTA，默认 10000）。
	// ★ 01a §5.5 约束1：设计不得依赖限时 100 万（逐月续期不保证）⇒ **默认按基线 1 万**；
	// 实际以管理后台「费用中心」为准，可经环境变量覆盖。
	FeishuMonthlyQuota int64
	// OrgSyncStaleHours 通讯录启动全量的新鲜度阈值（JX_ORG_SYNC_STALE_HOURS，默认 24）。
	// ★ docs/08 §4.5：距上次**成功**同步超阈值才拉（避免每次重启打一波）；
	//   启动全量为异步执行，失败不阻塞启动、不进就绪门禁。
	OrgSyncStaleHours int
	// OrgReconcileHours 通讯录定期对账阈值（JX_ORG_RECONCILE_HOURS，默认 168＝每周）。
	// ★ docs/08 §4.7 批次二：距上次**成功**全量超该阈值 ⇒ RunFull(trigger="reconcile")；
	//   对账＝以飞书为准（软删我方多出的、补齐我方缺失的），兜底事件丢投/漏处理漂移。
	//   消费端：orgsync.Runner.RunReconcileLoop。
	OrgReconcileHours int
}

// LoadEnv 从环境变量加载配置并填充默认值。
func LoadEnv() (*Env, error) {
	dataDir := getenv("JX_DATA_DIR", "./data")
	dbPath := getenv("JX_DB_PATH", filepath.Join(dataDir, "jxapproval.db"))
	lockPath := getenv("JX_LOCK_PATH", filepath.Join(dataDir, "jxapproval.lock"))

	intervalHours := getenvInt("JX_RECONCILE_INTERVAL_HOURS", 24)
	if intervalHours <= 0 {
		intervalHours = 24
	}

	orgSyncStaleHours := getenvInt("JX_ORG_SYNC_STALE_HOURS", 24)
	if orgSyncStaleHours <= 0 {
		orgSyncStaleHours = 24
	}

	orgReconcileHours := getenvInt("JX_ORG_RECONCILE_HOURS", 168)
	if orgReconcileHours <= 0 {
		orgReconcileHours = 168
	}

	// N-060 F2：审批对账周期（可配置；解析失败 ⇒ 默认 5m 可见回退）＋ 飞书月配额基数。
	approvalReconcileInterval := 5 * time.Minute
	if raw := strings.TrimSpace(getenv("JX_APPROVAL_RECONCILE_INTERVAL", "")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			approvalReconcileInterval = d
		}
	}
	feishuMonthlyQuota := int64(getenvInt("JX_FEISHU_MONTHLY_QUOTA", 10000))
	if feishuMonthlyQuota <= 0 {
		feishuMonthlyQuota = 10000
	}

	return &Env{
		AppID:                     getenv("JX_APP_ID", ""),
		AppSecret:                 getenv("JX_APP_SECRET", ""),
		DataDir:                   dataDir,
		AttachDir:                 getenv("JX_ATTACH_DIR", filepath.Join(dataDir, "attachments")),
		DBPath:                    dbPath,
		ListenAddr:                getenv("JX_LISTEN_ADDR", "127.0.0.1:8080"),
		SessionKey:                getenv("JX_SESSION_KEY", ""),
		InternalToken:             getenv("JX_INTERNAL_TOKEN", ""),
		CallbackDomain:            getenv("JX_CALLBACK_DOMAIN", ""),
		OAuthRedirectURI:          getenv("JX_OAUTH_REDIRECT_URI", ""),
		ActionCallbackToken:       getenv("JX_ACTION_CALLBACK_TOKEN", ""),
		LockPath:                  lockPath,
		S3Endpoint:                getenv("JX_S3_ENDPOINT", ""),
		S3Bucket:                  getenv("JX_S3_BUCKET", ""),
		S3Region:                  getenv("JX_S3_REGION", "us-east-1"),
		S3PathStyle:               getenvBool("JX_S3_PATH_STYLE", true),
		S3AK:                      getenv("JX_S3_AK", ""),
		S3SK:                      getenv("JX_S3_SK", ""),
		RustFSEndpoint:            getenv("JX_RUSTFS_ENDPOINT", ""),
		RustFSBucket:              getenv("JX_RUSTFS_BUCKET", getenv("JX_S3_BUCKET", "")),
		RustFSRegion:              getenv("JX_RUSTFS_REGION", getenv("JX_S3_REGION", "us-east-1")),
		RustFSPathStyle:           getenvBool("JX_RUSTFS_PATH_STYLE", getenvBool("JX_S3_PATH_STYLE", true)),
		RustFSAK:                  getenv("JX_RUSTFS_AK", ""),
		RustFSSK:                  getenv("JX_RUSTFS_SK", ""),
		RunEnv:                    getenv("JX_ENV", "prod"),
		DevMode:                   getenvBool("DEV_MODE", false),
		ReconcileInterval:         time.Duration(intervalHours) * time.Hour,
		ApprovalReconcileInterval: approvalReconcileInterval,
		FeishuMonthlyQuota:        feishuMonthlyQuota,
		OrgSyncStaleHours:         orgSyncStaleHours,
		OrgReconcileHours:         orgReconcileHours,
	}, nil
}

// IsDev 是否开发模式（决定是否注册 /internal/dev/inject-event）。
func (e *Env) IsDev() bool { return e.DevMode }

// FeishuRedirectURI 返回飞书免登回调地址：JX_OAUTH_REDIRECT_URI 优先，
// 否则缺省 JX_CALLBACK_DOMAIN + "/auth/feishu/callback"（去尾部斜杠后拼接）。
// 两者皆未配置 ⇒ 返回空串，调用方（authorize-url 端点）必须**可见报错**，不得静默编造。
func (e *Env) FeishuRedirectURI() string {
	if v := strings.TrimSpace(e.OAuthRedirectURI); v != "" {
		return v
	}
	if d := strings.TrimSpace(e.CallbackDomain); d != "" {
		return strings.TrimRight(d, "/") + "/auth/feishu/callback"
	}
	return ""
}

// IsTest 是否测试环境（test 开启可控时间窗，架构 §4.5）。
func (e *Env) IsTest() bool { return strings.EqualFold(e.RunEnv, "test") }

// MissingSecrets 返回尚未配置的敏感项键名列表（用于启动告警，不阻断开发模式）。
func (e *Env) MissingSecrets() []string {
	var missing []string
	check := func(key, val string) {
		if strings.TrimSpace(val) == "" {
			missing = append(missing, key)
		}
	}
	check("JX_APP_ID", e.AppID)
	check("JX_APP_SECRET", e.AppSecret)
	check("JX_SESSION_KEY", e.SessionKey)
	check("JX_INTERNAL_TOKEN", e.InternalToken)
	check("JX_ACTION_CALLBACK_TOKEN", e.ActionCallbackToken)
	return missing
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getenvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
