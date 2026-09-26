package permission

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// Loader 从配置表装载权限规则（口径变更不改代码，ADR-05）。
type Loader struct {
	db *store.DB

	mu    sync.RWMutex
	cache map[string]Rule // key: resource\x00role
}

// NewLoader 构造规则装载器。
func NewLoader(db *store.DB) *Loader {
	return &Loader{db: db, cache: map[string]Rule{}}
}

// Resolve 解析某角色对某资源的规则。未配置 → 返回 DENY 规则（deny by default，TC-32）。
func (l *Loader) Resolve(ctx context.Context, resource string, id Identity) (Rule, error) {
	role := strings.TrimSpace(id.Role)
	if role == "" {
		return Rule{Resource: resource, Role: role, RowScope: ScopeDeny}, nil
	}
	key := cacheKey(resource, role)
	l.mu.RLock()
	if r, ok := l.cache[key]; ok {
		l.mu.RUnlock()
		return r, nil
	}
	l.mu.RUnlock()

	st, err := l.db.GetPermissionRule(ctx, resource, role)
	if err != nil && errors.Is(err, store.ErrNotFound) {
		// 通配回退：文档中的资源枚举含 `ledger:*`（API §3.9），
		// 而业务侧按具体类型解析（如 `ledger:L01`）→ 精确未命中时再试 `ledger:*`。
		if wildcard := wildcardResource(resource); wildcard != "" && wildcard != resource {
			st, err = l.db.GetPermissionRule(ctx, wildcard, role)
		}
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Rule{Resource: resource, Role: role, RowScope: ScopeDeny}, nil
		}
		return Rule{}, err
	}
	rule := RuleFromStore(st)
	l.mu.Lock()
	l.cache[key] = rule
	l.mu.Unlock()
	return rule, nil
}

// Invalidate 清空缓存（规则表更新后调用）。
func (l *Loader) Invalidate() {
	l.mu.Lock()
	l.cache = map[string]Rule{}
	l.mu.Unlock()
}

func cacheKey(resource, role string) string {
	return strings.TrimSpace(resource) + "\x00" + strings.TrimSpace(role)
}

// wildcardResource 将「前缀:具体值」归约为「前缀:*」（如 ledger:L01 → ledger:*）。
// 无冒号或无前缀时返回空串（不做回退）。
func wildcardResource(resource string) string {
	r := strings.TrimSpace(resource)
	idx := strings.Index(r, ":")
	if idx <= 0 || idx == len(r)-1 {
		return ""
	}
	return r[:idx+1] + "*"
}
