package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

const defaultPageSize = 50
const maxPageSize = 200

// identityFrom 从会话实时解析身份与角色（不缓存决策，TC-11 / TC-32）。
func (d Deps) identityFrom(c echo.Context) (permission.Identity, *store.UserRole, error) {
	sess, ok := c.Get(ctxKeySession).(access.Session)
	if !ok {
		return permission.Identity{}, nil, errNoSession
	}
	ur, err := d.Auth.ResolveRole(c.Request().Context(), sess.OpenID)
	if err != nil {
		return permission.Identity{}, nil, err
	}
	return permission.Identity{
		OpenID:     ur.OpenID,
		Role:       ur.Role,
		Department: ur.Department,
		ExtraDepts: ur.ExtraDepts,
	}, ur, nil
}

var errNoSession = fmt.Errorf("httpapi: 无有效会话")

// audit 尽力写入审计日志（失败不阻断业务）。
func (d Deps) audit(ctx context.Context, row *store.AuditLogRow) {
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	if err := d.DB.InsertAudit(ctx, row); err != nil {
		d.Log.Warn("写审计日志失败", "error", err.Error())
	}
}

// pageParams 解析分页参数（page/page_size，默认 1/50，上限 200）。
func pageParams(c echo.Context) (page, size, offset int) {
	page = atoiDefault(c.QueryParam("page"), 1)
	if page < 1 {
		page = 1
	}
	size = atoiDefault(c.QueryParam("page_size"), defaultPageSize)
	if size < 1 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return page, size, (page - 1) * size
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// formatCents 将分格式化为带千分位的字符串（金额出参 * 元）。
func formatCents(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	yuan := cents / 100
	frac := cents % 100
	s := strconv.FormatInt(yuan, 10)
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	out := b.String() + "." + fmt.Sprintf("%02d", frac)
	if neg {
		out = "-" + out
	}
	return out
}

// ledgerRowMap 组装台账行（存档 + 运营合并视图）。
func ledgerRowMap(a store.LedgerArchive, ops map[string]any, flags map[string]any) map[string]any {
	archive := map[string]any{}
	if strings.TrimSpace(a.ExtJSON) != "" {
		_ = json.Unmarshal([]byte(a.ExtJSON), &archive)
	}
	row := map[string]any{
		"id":            a.ID,
		"ledger_type":   a.LedgerType,
		"biz_no":        a.BizNo,
		"department":    a.Department,
		"applicant":     a.ApplicantOpenID,
		"supplier":      a.Supplier,
		"biz_date":      a.BizDate,
		"instance_code": a.InstanceCode,
		"archive":       archive,
		"ops":           ops,
		"formula_flags": flags,
	}
	if a.AmountCents != nil {
		row["amount_cents"] = *a.AmountCents
		row["amount_display"] = formatCents(*a.AmountCents)
	}
	return row
}

// firstNonEmptyStr 返回首个非空（去空白后）字符串。
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// marshalStringList 把字符串切片序列化为 JSON 数组串（去空白、去空项、去重、升序）。
// 全空时返回空串——空串不是合法 JSON，行过滤侧会退化为等值比较（即不放行），符合 fail-closed。
func marshalStringList(vals []string) string {
	list := normaliseStringList(vals)
	if len(list) == 0 {
		return ""
	}
	b, err := json.Marshal(list)
	if err != nil {
		return ""
	}
	return string(b)
}

// normaliseStringList 去空白、去空项、去重、升序（幂等与序列化共用同一规范化，避免两处口径漂移）。
func normaliseStringList(vals []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		s := strings.TrimSpace(v)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// monthKey 把业务日期归一为月份的 `YYYY-MM` 键；无法识别时返回空串。
//
// ★ `biz_date` 全系统统一为 `YYYY-MM-DD`（B32）；此函数是**唯一**的月份归一入口，
// 避免各处再各自 `ReplaceAll` / 截位（历史上正是这样产生了 `"2026"`（整年）这种错前缀）。
func monthKey(v string) string {
	s := strings.TrimSpace(v)
	if len(s) >= 7 && (s[4] == '-' || s[4] == '/') {
		return s[:4] + "-" + s[5:7]
	}
	return ""
}

// instanceFilterCondition 组装实例行过滤条件。
func instanceRowCondition(rule permission.Rule, id permission.Identity) permission.Condition {
	return permission.RowFilterForInstances(rule.RowScope, id)
}

// parseISODate 把 any 形态的日期（string / time.Time）解析为 `YYYY-MM-DD` 日期。
// 解析不出来返回 false —— 调用方据此**不产出该字段**，而不是臆造成 0（延期天数等）。
func parseISODate(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{"2006-01-02", "2006/01/02", time.RFC3339} {
			if d, err := time.Parse(layout, s); err == nil {
				return d, true
			}
		}
	}
	return time.Time{}, false
}
