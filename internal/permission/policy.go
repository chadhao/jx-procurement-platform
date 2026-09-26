package permission

import "strings"

// Project 列级投影：在序列化阶段裁剪字段，无权限字段「连字段名都不出现」（TC-07）。
//
// 规则（架构 §5.3）：
//   - column_deny（黑名单）优先于 column_allow（白名单）；
//   - 敏感列（is_sensitive=1，如金额）默认进入 deny，除非本角色规则显式 allow；
//   - column_allow 为空表示「除 deny 外全部允许」。
func Project(row map[string]any, allow, deny []string, sensitive map[string]bool) map[string]any {
	denySet := toSet(deny)
	allowSet := toSet(allow)
	hasAllow := len(allowSet) > 0

	out := make(map[string]any, len(row))
	for k, v := range row {
		if denySet[k] {
			continue // deny 优先
		}
		if sensitive[k] {
			if !allowSet[k] {
				continue // 敏感列需显式 allow
			}
		} else if hasAllow && !allowSet[k] {
			continue
		}
		out[k] = v
	}
	return out
}

// CanWrite 判断某业务字段对该规则是否可写（仅运营表可写字段，TC-31）。
func CanWrite(rule Rule, field string) bool {
	for _, f := range rule.WritableFields {
		if strings.EqualFold(strings.TrimSpace(f), strings.TrimSpace(field)) {
			return true
		}
	}
	return false
}

// IsDenyAll 判断规则是否对全部行拒绝（无规则 / DENY 令牌）。
func IsDenyAll(rule Rule) bool {
	return rule.RowScope == ScopeDeny || strings.TrimSpace(string(rule.RowScope)) == ""
}

func toSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = true
		}
	}
	return out
}
