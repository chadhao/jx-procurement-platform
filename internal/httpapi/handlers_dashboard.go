package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/dashboard"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/submission"
)

// errBadPeriod 账期格式非法（YYYY-MM）→ 接口层映射为 40000。
var errBadPeriod = errors.New("看板：账期格式应为 YYYY-MM")

// dashboardError 将 buildDashboard 的错误映射为统一错误响应。
func dashboardError(c echo.Context, err error) error {
	if errors.Is(err, errBadPeriod) {
		return fail(c, http.StatusBadRequest, codeBadRequest, err.Error())
	}
	return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
}

// dashboardResource 看板 id → 权限资源（docs/05-API.md §3.3 / §3.9）。
// 分派规则：13→dashboard:1，14→dashboard:2，15→dashboard:3，16→dashboard:4。
func dashboardResource(id int) (string, bool) {
	switch id {
	case dashboard.DashboardBudget:
		return "dashboard:1", true
	case dashboard.DashboardPurchase:
		return "dashboard:2", true
	case dashboard.DashboardExpense:
		return "dashboard:3", true
	case dashboard.DashboardAnomaly:
		return "dashboard:4", true
	default:
		return "", false
	}
}

// resolveDashboard 解析看板 id 与该角色对应的权限规则。
// 角色对该资源无权限 → 已写 40300 响应并返回 ok=false（TC-14/A1）；不同角色得到不同数据集（FR-M5-08）。
func (d Deps) resolveDashboard(c echo.Context) (permission.Identity, permission.Rule, string, int, bool) {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		_ = fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
		return permission.Identity{}, permission.Rule{}, "", 0, false
	}
	id, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil {
		_ = fail(c, http.StatusBadRequest, codeBadRequest, "非法的看板 id")
		return permission.Identity{}, permission.Rule{}, "", 0, false
	}
	resource, ok := dashboardResource(id)
	if !ok {
		_ = fail(c, http.StatusNotFound, codeNotFound, "看板不存在: "+c.Param("id"))
		return permission.Identity{}, permission.Rule{}, "", 0, false
	}
	ctx := c.Request().Context()
	rule, err := d.Perm.Resolve(ctx, resource, idn)
	if err != nil {
		_ = fail(c, http.StatusInternalServerError, codeInternal, "权限规则读取失败")
		return permission.Identity{}, permission.Rule{}, "", 0, false
	}
	if permission.IsDenyAll(rule) {
		// ★ 无权限 → 40300 并留痕（TC-14）。
		d.audit(ctx, &store.AuditLogRow{
			ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
			Resource: resource, TargetID: c.Param("id"), Result: "deny",
		})
		_ = fail(c, http.StatusForbidden, codeForbidden, "无权限访问该看板")
		return permission.Identity{}, permission.Rule{}, "", 0, false
	}
	return idn, rule, resource, id, true
}

// buildDashboard 计算看板数据并施加列级投影（复用 permission.Project）。
func (d Deps) buildDashboard(c echo.Context, idn permission.Identity, rule permission.Rule, id int) (dashboard.Result, error) {
	ctx := c.Request().Context()
	// ★ 行级过滤：SQL 层（架构 §5.2 要点 1）。台账以别名 a 承载；实例表亦以别名 a 承载。
	ledgerCond := permission.RowFilter("a", rule.RowScope, idn)
	instCond := permission.RowFilterForInstances(rule.RowScope, idn)
	// ★ 报送表单独一路：它不在台账族内、也不在实例表内，漏掉会让以报送为数据源的
	// 聚合指标（「集团驳回后未处置」）退化为全表 COUNT，向受限角色暴露全局值。
	subCond := permission.RowFilterForSubmission(rule.RowScope, idn, "s")

	// 账期校验（P2）：非法账期直接 400，避免静默落到「空数据集」被误读为「本期无数据」。
	period := strings.TrimSpace(c.QueryParam("period"))
	if period != "" {
		if p, valid := submission.NormalizePeriod(period); valid {
			period = p
		} else {
			return dashboard.Result{}, errBadPeriod
		}
	}

	builder := dashboard.New(d.DB).WithNow(func() time.Time { return time.Now().UTC() })
	if d.Maps != nil {
		if t, ok := d.Maps.ThresholdCents("split_supplier_month"); ok && t > 0 {
			builder = builder.WithSplitThresholdCents(t)
		}
	}

	res, err := builder.Build(ctx, id, period, dashboard.Query{
		LedgerRowSQL:      ledgerCond.SQL,
		LedgerRowArgs:     ledgerCond.Args,
		InstanceRowSQL:    instCond.SQL,
		InstanceRowArgs:   instCond.Args,
		SubmissionRowSQL:  subCond.SQL,
		SubmissionRowArgs: subCond.Args,
	})
	if err != nil {
		return res, err
	}
	// ★ 列级投影：序列化阶段裁剪，无权限字段连字段名都不出现（TC-07）。
	d.projectResult(&res, rule.ColumnAllow, rule.ColumnDeny)
	return res, nil
}

// handleDashboard GET /api/dashboard/{id}（FR-M5-01/05/06/08）。
func (d Deps) handleDashboard(c echo.Context) error {
	idn, rule, resource, id, okRes := d.resolveDashboard(c)
	if !okRes {
		return nil
	}
	res, err := d.buildDashboard(c, idn, rule, id)
	if err != nil {
		return dashboardError(c, err)
	}
	// 看板只读：此处只记录 view 留痕，不提供任何写入口（FR-M5-06）。
	d.audit(c.Request().Context(), &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
		Resource: resource, TargetID: c.Param("id"), Result: "allow",
	})
	return ok(c, res)
}

// handleDashboardExport GET /api/dashboard/{id}/export（FR-M7-03；复用行·列投影器）。
func (d Deps) handleDashboardExport(c echo.Context) error {
	idn, rule, resource, id, okRes := d.resolveDashboard(c)
	if !okRes {
		return nil
	}
	format := strings.ToLower(strings.TrimSpace(c.QueryParam("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "xlsx" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "format 仅支持 csv / xlsx")
	}
	res, err := d.buildDashboard(c, idn, rule, id)
	if err != nil {
		return dashboardError(c, err)
	}

	// ★ 复用同一行·列投影器：禁止绕过列过滤（架构 §5.3 / TC-07 步骤 4）。
	flat := dashboard.Flatten(res)
	projected := make([]map[string]any, 0, len(flat))
	for _, row := range flat {
		projected = append(projected, permission.Project(row, rule.ColumnAllow, rule.ColumnDeny, nil))
	}
	headers := dashboard.UnionKeys(projected)
	table := dashboard.Stringify(headers, projected)

	// 导出留痕（FR-M7-03）。
	d.audit(c.Request().Context(), &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "export",
		Resource: resource, TargetID: res.Period, Result: "allow",
		DetailJSON: fmt.Sprintf(`{"dashboard":%d,"period":%q,"format":%q,"rows":%d}`, id, res.Period, format, len(table)),
	})

	filename := fmt.Sprintf("dashboard-%d-%s.%s", id, res.Period, format)
	switch format {
	case "xlsx":
		data, err := dashboard.FormatXLSX(headers, table)
		if err != nil {
			return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		}
		c.Response().Header().Set(echo.HeaderContentType,
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+filename+`"`)
		c.Response().WriteHeader(http.StatusOK)
		_, err = c.Response().Write(data)
		return err
	default:
		c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
		c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+filename+`"`)
		c.Response().WriteHeader(http.StatusOK)
		_, err = c.Response().Write(dashboard.FormatCSV(headers, table))
		return err
	}
}

// projectResult 对看板结果施加列级投影（金额类指标以 amount_cents / amount_display 命名）。
//
// ★ B19 修复：`Supervision` 原实现**只投影 `handler_concentration` 列表项**，其标量键
// （`requester_as_handler_count` / `concentration_max_count` / `handler_total` /
// `split_threshold_cents`）**完全不经过投影**；且用 `.([]map[string]any)` 做类型断言，
// **断言失败即静默跳过投影**（无日志、无报错）——属"投影漏了不报错、只会悄悄多给数据"的高危形态。
//
// 现改为：① 整块 Supervision 走 `ProjectDeep`（标量键与列表项一视同仁）；
//
//	② 去掉裸类型断言，异常类型**写 warn 日志**（不再静默跳过）。
func (d Deps) projectResult(res *dashboard.Result, allow, deny []string) {
	for i := range res.Cards {
		res.Cards[i] = permission.Project(res.Cards[i], allow, deny, nil)
	}
	for ci := range res.Charts {
		for si := range res.Charts[ci].Series {
			res.Charts[ci].Series[si] = permission.Project(res.Charts[ci].Series[si], allow, deny, nil)
		}
	}
	for i := range res.Alerts {
		res.Alerts[i] = permission.Project(res.Alerts[i], allow, deny, nil)
	}
	if res.Supervision == nil {
		return
	}
	// 整块投影：ProjectDeep 递归处理标量键与嵌套列表/对象，deny 与敏感列在任意层级生效。
	res.Supervision = permission.ProjectDeep(res.Supervision, allow, deny, nil)

	// 结构自检：`handler_concentration` 应为列表。类型异常只告警、不静默——它是投影正确性的信号。
	if v, exist := res.Supervision["handler_concentration"]; exist {
		switch v.(type) {
		case []any, []map[string]any, nil:
		default:
			d.Log.Warn("看板 Supervision.handler_concentration 类型异常，投影结果可能不完整",
				"got_type", fmt.Sprintf("%T", v))
		}
	}
}
