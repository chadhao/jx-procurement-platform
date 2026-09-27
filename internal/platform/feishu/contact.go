package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// contact.go —— 通讯录身份转换（user_id → open_id），docs/16 §2-A-3。
//
// ★ 背景（ID 域结论，docs/16 §0.3 已查证）：我方全库统存 **open_id**
//
//	（t_user_role.open_id / t_flow_task.assignee_open_id / access.Session.OpenID），
//	而飞书《三方快捷审批回调》官方发的是 **user_id（租户内域）**——二者**不同域、不可互比**。
//	回调拿到 user_id 后必须先换出 open_id 再进准入鉴权（admitCallback 的 operator==assignee）。
//
// ★★ 红线（docs/16 §2-A-3，逐字执行）：
//
//	① 绝不把 user_id 直接塞给 OperatorOpenID 参与比较 —— user_id 与 open_id 不同域，
//	  恒不命中 ⇒ 假 403（表面是「非本人」，实际是身份域错位，排障极其困难）；
//	② 转换失败 ⇒ **可见拒绝（HTTP 40000）＋ 告警日志**，绝不静默放行
//	  （空 operator 会被 HandleCallback 入口拒，行为一致），更不得占幂等键。
//
// ★ 实测状态（docs/16 §7 V-3，务必知悉）：`reference/README.md` 已实测 **contact/v3 域可用**、
//
//	所需 scope 候选为 `contact:contact.base:readonly` 等 5 个任一；但**「按 user_id 查询单个
//	用户」这一端点本身尚未联调实测**（V-3 待验证）——路径与 `user_id_type` 参数按官方
//	《通讯录》文档书写，联调首验后如有出入以实测为准。
//
// ★ 缓存：进程内 map + TTL（10 分钟）。回调频率低（人点一次才一发），进程内缓存即足够；
//	0012_org_directory（通讯录镜像）落地后可改走镜像，本期**不依赖**（docs/16 §2-A-3）。

// contactCacheTTL user_id → open_id 缓存有效期（docs/16 §2-A-3 建议 10 分钟）。
const contactCacheTTL = 10 * time.Minute

// ContactClient 身份转换端口（user_id → open_id）。
//
// 生产实现＝HTTPClient；测试/开发替身＝FakeContactClient。
// 上层（httpapi 回调 handler）只依赖本接口，不感知飞书报文（边界纪律同 ExternalApprovalClient）。
type ContactClient interface {
	// GetOpenIDByUserID 按租户内 user_id 换取 open_id。
	// 查无此人 / 接口失败 → 返回错误（调用方必须可见拒绝，绝不静默放行）。
	GetOpenIDByUserID(ctx context.Context, userID string) (string, error)
}

// GetOpenIDByUserID 按租户内 user_id 换取 open_id（GET /open-apis/contact/v3/users/{user_id}）。
//
// ★ 路径与参数按官方《通讯录》文档写：`GET /open-apis/contact/v3/users/:user_id?user_id_type=user_id`，
// 响应 `data.user.open_id` 即目标值。★★ **该端点待联调实测**（docs/16 §7 V-3；contact/v3 域
// 本身已实测可用，见 `reference/README.md`）——联调首验如有出入，以实测为准并回写此处注释。
func (c *HTTPClient) GetOpenIDByUserID(ctx context.Context, userID string) (string, error) {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		return "", fmt.Errorf("feishu: user_id→open_id 转换失败: user_id 为空")
	}
	// ① 进程内 TTL 缓存命中即返回（回调低频，进程内 map + TTL 足够，docs/16 §2-A-3）。
	if openID, ok := c.contactCacheGet(uid); ok {
		return openID, nil
	}
	// ② 调通讯录接口（唯一入站点在适配层）。
	data, _, err := c.doJSON(ctx, http.MethodGet,
		"/open-apis/contact/v3/users/"+url.PathEscape(uid),
		map[string]string{"user_id_type": "user_id"}, nil)
	if err != nil {
		return "", fmt.Errorf("feishu: user_id→open_id 转换失败（通讯录接口）: %w", err)
	}
	var out struct {
		User struct {
			OpenID string `json:"open_id"`
			UserID string `json:"user_id"`
		} `json:"user"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return "", fmt.Errorf("feishu: 解析通讯录用户响应失败: %w", err)
		}
	}
	openID := strings.TrimSpace(out.User.OpenID)
	if openID == "" {
		// 查无此人 / open_id 为空：显式失败（调用方可见拒绝），绝不返回空值让上层静默放行。
		return "", fmt.Errorf("feishu: user_id=%s 换不出 open_id（用户不存在或应用无可见范围）", uid)
	}
	c.contactCachePut(uid, openID)
	return openID, nil
}

// ---------- 进程内 TTL 缓存（挂在 HTTPClient 上，见 client.go 的字段定义）----------

// contactCacheGet 读取缓存（惰性过期：过期项顺带删除）。
func (c *HTTPClient) contactCacheGet(userID string) (string, bool) {
	c.contactMu.Lock()
	defer c.contactMu.Unlock()
	ent, ok := c.contactCache[userID]
	if !ok {
		return "", false
	}
	if time.Now().After(ent.expireAt) {
		delete(c.contactCache, userID)
		return "", false
	}
	return ent.openID, true
}

// contactCachePut 写入缓存（带上限守卫：防止极端量级下 map 无界膨胀）。
func (c *HTTPClient) contactCachePut(userID, openID string) {
	c.contactMu.Lock()
	defer c.contactMu.Unlock()
	if c.contactCache == nil {
		c.contactCache = map[string]contactCacheEntry{}
	}
	if len(c.contactCache) >= contactCacheMaxEntries {
		// 简单过期清扫；清扫后仍满则丢弃本次写入（缓存语义，丢写不影响正确性）。
		now := time.Now()
		for k, v := range c.contactCache {
			if now.After(v.expireAt) {
				delete(c.contactCache, k)
			}
		}
		if len(c.contactCache) >= contactCacheMaxEntries {
			return
		}
	}
	c.contactCache[userID] = contactCacheEntry{openID: openID, expireAt: time.Now().Add(contactCacheTTL)}
}

// contactCacheMaxEntries 缓存条目上限（11 类审批 × 少量审批人，1000 绰绰有余）。
const contactCacheMaxEntries = 1000

// contactCacheEntry 单条缓存（值 + 过期时刻）。
type contactCacheEntry struct {
	openID   string
	expireAt time.Time
}

// ---------- 测试 / 开发替身（对齐 fake.go 惯例：Fn 钩子 + 安全默认值）----------

// FakeContactClient 是 ContactClient 的内存测试替身。
//
// 通过 Mappings 预置映射（或设 ResolveFn 钩子控制行为）；未配置时**默认报错** ——
// 与生产语义对齐（查不出 open_id 必须**可见失败**，绝不静默放行、绝不返回 user_id 冒充 open_id）。
type FakeContactClient struct {
	mu sync.Mutex
	// Mappings 预置 user_id → open_id 映射（测试直接查表）。
	Mappings map[string]string
	// ResolveFn 覆盖行为（如模拟接口失败 / 超时）；nil 时按 Mappings 查表。
	ResolveFn func(ctx context.Context, userID string) (string, error)
	// Calls 调用次数（测试断言缓存生效用：同一 user_id 第二次不再 +1）。
	Calls int
}

var _ ContactClient = (*FakeContactClient)(nil)

// GetOpenIDByUserID 按 Mappings / ResolveFn 解析；查不到 → 显式错误（不静默）。
func (f *FakeContactClient) GetOpenIDByUserID(ctx context.Context, userID string) (string, error) {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		return "", fmt.Errorf("fake: user_id 为空")
	}
	if f.ResolveFn != nil {
		return f.ResolveFn(ctx, uid)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if openID, ok := f.Mappings[uid]; ok {
		return openID, nil
	}
	return "", fmt.Errorf("fake: user_id=%s 无映射（查不出 open_id）", uid)
}

// CallCount 返回累计调用次数（缓存断言用）。
func (f *FakeContactClient) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls
}
