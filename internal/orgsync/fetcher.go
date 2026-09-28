// Package orgsync 飞书通讯录镜像同步（docs/08-Org-Sync-Design.md 实施批次一）。
//
// ★ 职责：全量拉取（部门递归 + 各部门用户）→ 落镜像（幂等、软删只增不减、失败可见）。
// 事件增量与每周对账属批次二（先出方案不实施，见交付报告）。
//
// ★★ 结构性红线（docs/08 §4.10）：本包只依赖 store 的镜像方法，**不得** import
// internal/permission / internal/access，**不得**写 t_user_role（门禁：grep 断言 0 命中）。
package orgsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ★ 官方接口核对（2026-09-28，open.feishu.cn 一级来源；详见交付报告「官方 API 核对表」）：
//
//   部门列表：GET /open-apis/contact/v3/departments
//     - parent_department_id（根=0）、fetch_child（递归，默认 false）、
//       department_id_type（默认 open_department_id）、page_size（默认 10，最大 50）、page_token。
//     - 响应 data.has_more / data.page_token / data.items[]（**无 total**；同项含
//       department_id 与 open_department_id 双 ID——本仓库 2026-09-27 实测印证）。
//     - 错误 43010「超大部门不允许递归」（小规模租户不触发；触发即显式失败，不静默）。
//   部门用户：GET /open-apis/contact/v3/users/find_by_department
//     - department_id（必填，类型随 department_id_type；根=0）、page_size（最大 50）、page_token。
//   用户字段：open_id / user_id / union_id / name(string) / department_ids(string[],
//     元素为 od- 前缀 open_department_id) / status{is_activated,is_frozen,is_resigned,is_exited,is_unjoin}。
//   频控：1000 次/分钟、50 次/秒（全量约 部门数+部门数×分页 次调用，规模远低于频控）。
//   scope：任一即可——「获取通讯录基本信息 / 获取通讯录部门组织架构信息 /
//     以应用身份读取通讯录 / 以应用身份访问通讯录」（本仓库已实测 contact/v3 域可用，
//     候选 5 个见 docs/reference/README.md；name/department_ids 属字段级权限，
//     未开通时字段为空 → 计入 field_gaps 可见，不静默）。

// pageTokenGuard 分页循环上限（防远端异常回环时无界请求；触发即显式报错）。
const pageTokenGuard = 10000

// pageSize 官方分页上限（部门列表与用户列表同为 50，2026-09-28 官方文档核对）。
const pageSize = 50

// Fetcher 通讯录拉取端口（docs/08 §4.3 DirectoryFetcher 的落地形态：
// 部门一次递归拉全；用户按部门逐个拉取，由 Runner 编排）。
type Fetcher interface {
	// ListDepartments 拉取全部部门（含根部门 "0"）。分页由实现内部处理；
	// 任何一页失败 ⇒ 返回错误（不得静默返回部分结果）。
	ListDepartments(ctx context.Context) ([]*store.OrgDepartment, error)
	// ListUsersByDepartment 拉取某部门（open_department_id）下的用户列表。
	ListUsersByDepartment(ctx context.Context, openDepartmentID string) ([]*store.OrgUser, error)
}

// TokenSource tenant_access_token 来源（生产＝feishu.HTTPClient 共享缓存；测试＝桩）。
type TokenSource interface {
	TenantAccessToken(ctx context.Context) (string, error)
}

// TokenSourceFunc 函数适配器。
type TokenSourceFunc func(ctx context.Context) (string, error)

// TenantAccessToken 实现 TokenSource。
func (f TokenSourceFunc) TenantAccessToken(ctx context.Context) (string, error) {
	return f(ctx)
}

// FeishuFetcher Fetcher 的官方 HTTP 实现（contact/v3 族）。
//
// 独立小客户端而非复用 feishu.HTTPClient.doJSON：doJSON 未导出且其 baseURL 固定，
// 而本包必须能以 httptest 替身服务器做不依赖真机的单测（任务自验硬要求）。
// token 经 TokenSource 与业务侧共享同一缓存，无额外凭据面。
type FeishuFetcher struct {
	baseURL string
	tokens  TokenSource
	hc      *http.Client
	log     *slog.Logger
}

// NewFeishuFetcher 构造；baseURL 为空时取飞书开放平台默认域名。
func NewFeishuFetcher(tokens TokenSource, baseURL string, log *slog.Logger) *FeishuFetcher {
	if baseURL == "" {
		baseURL = "https://open.feishu.cn"
	}
	if log == nil {
		log = slog.Default()
	}
	return &FeishuFetcher{baseURL: strings.TrimRight(baseURL, "/"), tokens: tokens,
		hc: &http.Client{}, log: log}
}

// apiEnvelope 飞书统一响应包裹（与 feishu.HTTPClient 同形）。
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// doGet 发起带鉴权 GET 并解析统一包裹，返回 data 段。
func (f *FeishuFetcher) doGet(ctx context.Context, path string, query map[string]string) (json.RawMessage, error) {
	token, err := f.tokens.TenantAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("orgsync: 获取 tenant_access_token 失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("orgsync: 调用 %s 失败: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("orgsync: 读取 %s 响应失败: %w", path, err)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("orgsync: 解析 %s 响应失败: %w", path, err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("orgsync: %s 返回错误 code=%d msg=%s", path, env.Code, env.Msg)
	}
	return env.Data, nil
}

// pager 分页响应公共形态（官方响应无 total，以 has_more 判继续——本仓库实测印证）。
type pager struct {
	HasMore   bool            `json:"has_more"`
	PageToken string          `json:"page_token"`
	Items     json.RawMessage `json:"items"`
}

// nextToken 校验并返回下一页 token：has_more=true 而 page_token 为空 ⇒ 显式报错
// （防无界循环，也不静默截断——截断即「部分成功伪装成成功」，静默缺陷族）。
func nextToken(p *pager, pages int, where string) (string, error) {
	if !p.HasMore {
		return "", nil
	}
	if strings.TrimSpace(p.PageToken) == "" {
		return "", fmt.Errorf("orgsync: %s has_more=true 但未返回 page_token（已拉 %d 页）", where, pages)
	}
	if pages >= pageTokenGuard {
		return "", fmt.Errorf("orgsync: %s 分页超过 %d 页上限，疑似回环，中止", where, pages)
	}
	return p.PageToken, nil
}

// deptItem 部门列表响应项（只取镜像所需字段；原文留 raw）。
type deptItem struct {
	OpenDepartmentID   string `json:"open_department_id"`
	DepartmentID       string `json:"department_id"`
	Name               string `json:"name"`
	ParentDepartmentID string `json:"parent_department_id"`
}

// userItem 用户响应项（字段名经官方文档核对，见包注释）。
type userItem struct {
	OpenID        string   `json:"open_id"`
	UserID        string   `json:"user_id"`
	UnionID       string   `json:"union_id"`
	Name          string   `json:"name"`
	DepartmentIDs []string `json:"department_ids"`
	Status        struct {
		IsActivated bool `json:"is_activated"`
		IsFrozen    bool `json:"is_frozen"`
		IsResigned  bool `json:"is_resigned"`
		IsExited    bool `json:"is_exited"`
		IsUnjoin    bool `json:"is_unjoin"`
	} `json:"status"`
}

// rootDepartmentID 根部门 ID（官方：根部门的部门 ID 为 0）。
const rootDepartmentID = "0"

// ListDepartments 拉取全部部门：parent_department_id=0 + fetch_child=true 一次递归拉全，
// 按 has_more/page_token 翻页；末尾补根部门记录（递归列表是否含根未在官方页明确 ——
// 显式补齐而非假设，保证「根直属用户」有拉取锚点）。
func (f *FeishuFetcher) ListDepartments(ctx context.Context) ([]*store.OrgDepartment, error) {
	out := []*store.OrgDepartment{}
	seenRoot := false
	pageToken := ""
	for page := 1; ; page++ {
		query := map[string]string{
			"parent_department_id": rootDepartmentID,
			"fetch_child":          "true",
			"department_id_type":   "open_department_id",
			"page_size":            strconv.Itoa(pageSize),
		}
		if pageToken != "" {
			query["page_token"] = pageToken
		}
		data, err := f.doGet(ctx, "/open-apis/contact/v3/departments", query)
		if err != nil {
			return nil, fmt.Errorf("orgsync: 拉取部门列表第 %d 页失败: %w", page, err)
		}
		var p pager
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("orgsync: 解析部门列表第 %d 页失败: %w", page, err)
		}
		var raws []json.RawMessage
		if len(p.Items) > 0 {
			if err := json.Unmarshal(p.Items, &raws); err != nil {
				return nil, fmt.Errorf("orgsync: 解析部门列表第 %d 页 items 失败: %w", page, err)
			}
			for _, raw := range raws {
				var it deptItem
				if err := json.Unmarshal(raw, &it); err != nil {
					return nil, fmt.Errorf("orgsync: 解析部门列表第 %d 页单条失败: %w", page, err)
				}
				if it.OpenDepartmentID == rootDepartmentID {
					seenRoot = true
					continue // 根部门记录统一在末尾补齐（保证单条、不依赖远端行为）
				}
				out = append(out, &store.OrgDepartment{
					OpenDepartmentID:       it.OpenDepartmentID,
					DepartmentID:           it.DepartmentID,
					ParentOpenDepartmentID: it.ParentDepartmentID,
					Name:                   it.Name,
					RawJSON:                rawOf(raw),
				})
			}
		}
		token, err := nextToken(&p, page, "部门列表")
		if err != nil {
			return nil, err
		}
		if token == "" {
			break
		}
		pageToken = token
	}
	if !seenRoot {
		out = append([]*store.OrgDepartment{{
			OpenDepartmentID: rootDepartmentID,
			Name:             "",
			RawJSON:          "{}",
		}}, out...)
	}
	return out, nil
}

// rawJon 保留项原文用（json.RawMessage → string）。
func rawOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// ListUsersByDepartment 拉取某部门（含根部门 0）下的用户，按 has_more/page_token 翻页。
// ★ 单页失败 ⇒ 整体报错（不得静默返回部分人员——部分成功伪装成成功属静默缺陷族）。
func (f *FeishuFetcher) ListUsersByDepartment(ctx context.Context, openDepartmentID string) ([]*store.OrgUser, error) {
	deptID := strings.TrimSpace(openDepartmentID)
	if deptID == "" {
		return nil, fmt.Errorf("orgsync: ListUsersByDepartment: open_department_id 为空")
	}
	out := []*store.OrgUser{}
	pageToken := ""
	for page := 1; ; page++ {
		query := map[string]string{
			"department_id":      deptID,
			"department_id_type": "open_department_id",
			"page_size":          strconv.Itoa(pageSize),
		}
		if pageToken != "" {
			query["page_token"] = pageToken
		}
		data, err := f.doGet(ctx, "/open-apis/contact/v3/users/find_by_department", query)
		if err != nil {
			return nil, fmt.Errorf("orgsync: 拉取部门 %s 用户第 %d 页失败: %w", deptID, page, err)
		}
		var p pager
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("orgsync: 解析部门 %s 用户第 %d 页失败: %w", deptID, page, err)
		}
		var raws []json.RawMessage
		if len(p.Items) > 0 {
			if err := json.Unmarshal(p.Items, &raws); err != nil {
				return nil, fmt.Errorf("orgsync: 解析部门 %s 用户第 %d 页 items 失败: %w", deptID, page, err)
			}
			for _, raw := range raws {
				var it userItem
				if err := json.Unmarshal(raw, &it); err != nil {
					return nil, fmt.Errorf("orgsync: 解析部门 %s 用户第 %d 页单条失败: %w", deptID, page, err)
				}
				out = append(out, &store.OrgUser{
					OpenID:              it.OpenID,
					UnionID:             it.UnionID,
					UserID:              it.UserID,
					Name:                it.Name,
					EmployeeStatus:      normalizeStatus(it.Status.IsResigned, it.Status.IsExited, it.Status.IsFrozen, it.Status.IsActivated, it.Status.IsUnjoin),
					IsResigned:          it.Status.IsResigned,
					IsExited:            it.Status.IsExited,
					IsFrozen:            it.Status.IsFrozen,
					IsActivated:         it.Status.IsActivated,
					IsUnjoin:            it.Status.IsUnjoin,
					PrimaryDepartmentID: primaryDeptOf(it.DepartmentIDs),
					DepartmentIDs:       it.DepartmentIDs,
					RawJSON:             rawOf(raw),
				})
			}
		}
		token, err := nextToken(&p, page, "部门 "+deptID+" 用户")
		if err != nil {
			return nil, err
		}
		if token == "" {
			break
		}
		pageToken = token
	}
	return out, nil
}

// primaryDeptOf 主部门＝department_ids 首项（docs/08 §4.2；元素为 od- 前缀 open_department_id）。
func primaryDeptOf(ids []string) string {
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			return id
		}
	}
	return ""
}

// normalizeStatus 归一在职态（docs/08 §4.2：在职/离职/冻结/未激活/未入职）。
// 优先级：离职 > 冻结 > 未入职 > 未激活 > 在职（离/冻/未入职是「不可用」态，先判）。
func normalizeStatus(resigned, exited, frozen, activated, unjoin bool) string {
	switch {
	case resigned || exited:
		return "离职"
	case frozen:
		return "冻结"
	case unjoin:
		return "未入职"
	case !activated:
		return "未激活"
	default:
		return "在职"
	}
}
