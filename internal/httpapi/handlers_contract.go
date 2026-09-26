package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 变更链回溯（M4，FR-M4-07；制度第五十二条）。
//
// 口径：变更单按**合同号**关联，可按合同号回溯「历次变更的次数 / 累计金额 / 所取档位」。
// ★ 变更审批档位 = **max（变更差额, 原合同金额）**（批复 A8，化整为零通道已关闭）——
// 本接口把每一笔的「变更差额 / 原合同金额 / 取用档位」三者一并回出，便于核对是否按 max 取档。
// 权限：沿用台账口径（资源 `ledger:L09` 例外事项台账），行级过滤在 SQL 层、列级投影在序列化层。

// changeAmountKeys / originalAmountKeys / tierKeys —— ext_json 中可能的键名候选。
//
// ★ Q14 台账字段口径未定稿，故按候选键取值而非写死单键（与 store.ListChangesByContract
// 的「键名无关匹配」配套）。定稿后收敛为单键并删去候选。
var (
	changeAmountKeys   = []string{"change_cents", "change_amount_cents", "delta_cents", "change_amount"}
	originalAmountKeys = []string{"original_cents", "contract_cents", "original_amount_cents", "original_amount"}
	tierKeys           = []string{"tier", "approval_tier", "approval_level", "档位"}
)

// jsonNumber 从 any 提取整数（兼容 JSON number / 数字字符串 / float）。
func jsonNumber(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case int:
		return int64(x), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
	case string:
		if s := strings.TrimSpace(x); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n, true
			}
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return int64(f), true
			}
		}
	}
	return 0, false
}

// pickInt 按候选键顺序取第一个可解析为整数的值。
func pickInt(m map[string]any, keys []string) (int64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if n, ok2 := jsonNumber(v); ok2 {
				return n, true
			}
		}
	}
	return 0, false
}

// pickStr 按候选键顺序取第一个非空字符串。
func pickStr(m map[string]any, keys []string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok2 := v.(string); ok2 && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// handleContractChanges GET /api/contract/:biz_no/changes（FR-M4-07）。
func (d Deps) handleContractChanges(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	contractNo := strings.TrimSpace(c.Param("biz_no"))
	if contractNo == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "合同号不能为空")
	}

	// 权限与台账一致：以例外事项台账（L09）为资源解析行·列规则。
	rule, err := d.Perm.Resolve(ctx, "ledger:L09", idn)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, "权限规则读取失败")
	}
	if permission.IsDenyAll(rule) {
		d.audit(ctx, &store.AuditLogRow{
			ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
			Resource: "ledger:L09", TargetID: contractNo, Result: "deny",
		})
		return fail(c, http.StatusForbidden, codeForbidden, "无权限访问变更链")
	}

	cond := permission.RowFilter("a", rule.RowScope, idn)
	changes, err := d.DB.ListChangesByContract(ctx, contractNo, cond.SQL, cond.Args)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	sensitive, _ := d.DB.SensitiveFields(ctx, "L09")

	var (
		cumulativeChange int64
		cumulativeKnown  bool
		count            = len(changes)
		items            = make([]map[string]any, 0, len(changes))
	)
	for _, ch := range changes {
		var ext map[string]any
		if strings.TrimSpace(ch.ExtJSON) != "" {
			_ = json.Unmarshal([]byte(ch.ExtJSON), &ext)
		}
		changeCents, hasChange := pickInt(ext, changeAmountKeys)
		originalCents, hasOriginal := pickInt(ext, originalAmountKeys)
		// 变更金额兜底：ext_json 未给差额时，退用存档行的 amount_cents（变更单自身金额）。
		if !hasChange && ch.AmountCents != nil {
			changeCents = *ch.AmountCents
			hasChange = true
		}
		// 档位：显式存了就用存的；否则按 max(变更差额, 原合同金额) 现算（A8）。
		tier := pickStr(ext, tierKeys)
		if tier == "" && hasChange && hasOriginal {
			m := changeCents
			if originalCents > m {
				m = originalCents
			}
			tier = "max 取档 → " + formatCents(m)
		}
		if hasChange {
			cumulativeChange += changeCents
			cumulativeKnown = true
		}

		item := map[string]any{
			"biz_no":        ch.BizNo,
			"instance_code": ch.InstanceCode,
			"department":    ch.Department,
			"biz_date":      ch.BizDate,
			"tier":          tier,
			"archive":       ext,
		}
		if hasChange {
			item["change_cents"] = changeCents
			item["change_display"] = formatCents(changeCents)
		}
		if hasOriginal {
			item["original_cents"] = originalCents
			item["original_display"] = formatCents(originalCents)
		}
		items = append(items, permission.Project(item, rule.ColumnAllow, rule.ColumnDeny, sensitive))
	}

	// ★ 禁金额角色：不仅 `amount_cents` 要被裁，**变更链自己的金额键名**（`change_cents` /
	// `original_cents` / `*_display`）与 `archive` 内嵌金额也必须一并消失（B31）。
	// 投影层已按「金额类键名」统一裁剪（见 permission.ProjectDeep）；此处只负责不再额外输出汇总金额。
	hideAmounts := permission.RuleHidesAmounts(rule)

	body := map[string]any{
		"contract_no":   contractNo,
		"count":         count,
		"items":         items,
		"amount_hidden": hideAmounts || (!cumulativeKnown && count > 0),
	}
	if cumulativeKnown && !hideAmounts {
		body["cumulative_change_cents"] = cumulativeChange
		body["cumulative_change_display"] = formatCents(cumulativeChange)
	}

	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
		Resource: "ledger:L09", TargetID: contractNo, Result: "allow",
		DetailJSON: `{"changes":` + strconv.Itoa(count) + `}`,
	})
	return ok(c, body)
}
