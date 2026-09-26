package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ---------- 免登与会话 ----------

// handleFeishuCallback 飞书免登回调；未映射角色的 open_id 拒绝进入业务页（TC-32）。
// 开发模式（DEV_MODE=true）允许以 ?open_id= 直接建立会话，用于无凭据的端到端验证。
func (d Deps) handleFeishuCallback(c echo.Context) error {
	ctx := c.Request().Context()
	code := c.QueryParam("code")
	state := c.QueryParam("state")
	devOpenID := c.QueryParam("open_id")

	if strings.TrimSpace(state) == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "缺少 state（防 CSRF）")
	}
	if strings.TrimSpace(code) == "" && !(d.Auth.DevMode() && strings.TrimSpace(devOpenID) != "") {
		return fail(c, http.StatusBadRequest, codeBadRequest, "缺少 code")
	}

	ident, err := d.Auth.Exchange(ctx, code, devOpenID)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeUnauthorized, "免登失败: "+err.Error())
	}

	ur, err := d.Auth.ResolveRole(ctx, ident.OpenID)
	if err != nil {
		// ★ 默认拒绝：未映射角色不得进入业务页。
		d.audit(ctx, &store.AuditLogRow{ActorOpenID: ident.OpenID, Action: "login",
			Resource: "auth", Result: "deny", DetailJSON: `{"reason":"role_not_mapped"}`})
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未配置角色，请联系系统管理员")
	}

	value := d.Auth.Establish(ident.OpenID)
	setSessionCookie(c, value, d.Env, int((8 * time.Hour).Seconds()))
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: ident.OpenID, ActorRole: ur.Role, Action: "login", Resource: "auth", Result: "allow"})
	return c.Redirect(http.StatusFound, "/")
}

// handleLogout 注销会话。
func (d Deps) handleLogout(c echo.Context) error {
	if ck, err := c.Cookie(access.CookieName); err == nil && ck.Value != "" {
		if sess, ok := d.Auth.ResolveSession(ck.Value); ok {
			d.Auth.Logout(sess.ID)
		}
	}
	setSessionCookie(c, "", d.Env, -1)
	return ok(c, map[string]any{"logged_out": true})
}

// handleMe 返回当前会话身份、角色与可见范围摘要（真拦截在服务端）。
func (d Deps) handleMe(c echo.Context) error {
	idn, ur, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	rules, _ := d.DB.ListPermissionRules(ctx)
	rowScopes := map[string]string{}
	denied := map[string]bool{}
	for _, r := range rules {
		if !strings.EqualFold(r.Role, ur.Role) {
			continue
		}
		rowScopes[r.Resource] = r.RowScope
		for _, c := range r.ColumnDeny {
			denied[c] = true
		}
	}
	denyList := make([]string, 0, len(denied))
	for k := range denied {
		denyList = append(denyList, k)
	}
	departments := append([]string{ur.Department}, ur.ExtraDepts...)
	return ok(c, map[string]any{
		"open_id":     idn.OpenID,
		"name":        ur.Name,
		"role":        ur.Role,
		"departments": departments,
		"column_policy_summary": map[string]any{
			"column_deny": denyList,
			"row_scopes":  rowScopes,
		},
	})
}

// ---------- 实例（M2） ----------

func (d Deps) handleListInstances(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	rule, err := d.Perm.Resolve(ctx, "api:instances", idn)
	if err != nil {
		return fail(c, http.StatusBadRequest, codeForbidden, "无权访问实例")
	}
	if permission.IsDenyAll(rule) {
		return fail(c, http.StatusForbidden, codeForbidden, "无权访问实例")
	}
	cond := instanceRowCondition(rule, idn)
	page, size, offset := pageParams(c)

	items, total, err := d.DB.ListInstances(ctx, store.InstanceFilter{
		ApprovalCode: c.QueryParam("approval_code"),
		DocType:      c.QueryParam("doc_type"),
		Status:       c.QueryParam("status"),
		Department:   c.QueryParam("department"),
		RowSQL:       cond.SQL,
		RowArgs:      cond.Args,
		Limit:        size,
		Offset:       offset,
	})
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		row := instanceRowMap(it)
		row = permission.Project(row, rule.ColumnAllow, rule.ColumnDeny, nil)
		out = append(out, row)
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view", Resource: "instances", Result: "allow"})
	return ok(c, map[string]any{"items": out, "total": total, "page": page, "page_size": size})
}

func (d Deps) handleGetInstance(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	rule, err := d.Perm.Resolve(ctx, "api:instances", idn)
	if err != nil || permission.IsDenyAll(rule) {
		return fail(c, http.StatusForbidden, codeForbidden, "无权访问实例")
	}
	code := c.Param("code")
	allowed, err := d.instanceAllowed(ctx, rule, idn, code)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	if !allowed {
		d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view", Resource: "instances", TargetID: code, Result: "deny"})
		return fail(c, http.StatusForbidden, codeRowForbidden, "越权访问：该实例不在你的可见范围内")
	}
	inst, err := d.DB.GetInstance(ctx, code)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "实例不存在")
	}
	row := permission.Project(instanceRowMap(*inst), rule.ColumnAllow, rule.ColumnDeny, nil)
	return ok(c, row)
}

func (d Deps) handleInstanceFields(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	rule, _ := d.Perm.Resolve(ctx, "api:instances", idn)
	code := c.Param("code")
	if allowed, err := d.instanceAllowed(ctx, rule, idn, code); err != nil || !allowed {
		return fail(c, http.StatusForbidden, codeRowForbidden, "越权访问：该实例不在你的可见范围内")
	}
	fields, err := d.DB.ListFields(ctx, code)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	out := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		var biz any
		if strings.TrimSpace(f.BizField) != "" {
			biz = f.BizField
		}
		out = append(out, map[string]any{
			"field_id":   f.FieldID, // ★ 具体值待确认（Q1）
			"field_name": f.FieldName,
			"biz_field":  biz,
			"value":      f.ValueText,
			"value_type": f.ValueType,
		})
	}
	return ok(c, map[string]any{"fields": out})
}

func (d Deps) handleInstanceTimeline(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	rule, _ := d.Perm.Resolve(ctx, "api:instances", idn)
	code := c.Param("code")
	if allowed, err := d.instanceAllowed(ctx, rule, idn, code); err != nil || !allowed {
		return fail(c, http.StatusForbidden, codeRowForbidden, "越权访问：该实例不在你的可见范围内")
	}
	hist, err := d.DB.ListHistory(ctx, code)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	events := make([]map[string]any, 0, len(hist))
	for _, h := range hist {
		events = append(events, map[string]any{
			"status":      h.Status,
			"task_node":   h.TaskNode,
			"operator":    h.OperatorOpenID,
			"opinion":     h.Opinion,
			"occurred_at": h.OccurredAt.Format(time.RFC3339),
			"event_seq":   h.EventSeq,
		})
	}
	return ok(c, map[string]any{"events": events})
}

// ---------- 台账（M4） ----------

func (d Deps) handleLedgerList(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	table := c.Param("table")
	rule, err := d.Perm.Resolve(ctx, "ledger:"+table, idn)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, "权限规则读取失败")
	}
	if permission.IsDenyAll(rule) {
		d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view", Resource: "ledger:" + table, Result: "deny"})
		return fail(c, http.StatusForbidden, codeForbidden, "无权限访问该台账")
	}
	cond := permission.RowFilter("a", rule.RowScope, idn)
	sensitive, _ := d.DB.SensitiveFields(ctx, table)
	page, size, offset := pageParams(c)

	rows, total, err := d.DB.ListArchive(ctx, store.LedgerFilter{
		LedgerType: table,
		BizNo:      c.QueryParam("biz_no"),
		Department: c.QueryParam("department"),
		Supplier:   c.QueryParam("supplier"),
		DateFrom:   c.QueryParam("date_from"),
		DateTo:     c.QueryParam("date_to"),
		RowSQL:     cond.SQL,
		RowArgs:    cond.Args,
		Limit:      size,
		Offset:     offset,
	})
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	items := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		ops := d.loadOps(ctx, table, a.BizNo)
		row := ledgerRowMap(a, ops, d.formulaFlags(ctx, table, a))
		// ★ 列级投影在序列化阶段裁剪，无权限字段连字段名都不出现（TC-07）。
		row = permission.Project(row, rule.ColumnAllow, rule.ColumnDeny, sensitive)
		items = append(items, row)
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view", Resource: "ledger:" + table, Result: "allow"})
	return ok(c, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}

func (d Deps) handleLedgerGet(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	table := c.Param("table")
	rule, err := d.Perm.Resolve(ctx, "ledger:"+table, idn)
	if err != nil || permission.IsDenyAll(rule) {
		return fail(c, http.StatusForbidden, codeForbidden, "无权限访问该台账")
	}
	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的记录 id")
	}
	sensitive, _ := d.DB.SensitiveFields(ctx, table)
	cond := permission.RowFilter("a", rule.RowScope, idn)

	if allowed, err := d.archiveAllowed(ctx, cond, id); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	} else if !allowed {
		d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view", Resource: "ledger:" + table, TargetID: c.Param("id"), Result: "deny"})
		return fail(c, http.StatusForbidden, codeRowForbidden, "越权访问：该记录不在你的可见范围内")
	}
	a, err := d.DB.GetArchiveByID(ctx, id)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "记录不存在")
	}
	ops := d.loadOps(ctx, table, a.BizNo)
	row := permission.Project(ledgerRowMap(*a, ops, d.formulaFlags(ctx, table, *a)), rule.ColumnAllow, rule.ColumnDeny, sensitive)
	return ok(c, row)
}

func (d Deps) handleLedgerPatch(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	table := c.Param("table")
	rule, err := d.Perm.Resolve(ctx, "ledger:"+table, idn)
	if err != nil || permission.IsDenyAll(rule) {
		return fail(c, http.StatusForbidden, codeForbidden, "无权限访问该台账")
	}
	// ★ 同步存档表无写入口：仅当规则配置了可写字段时才允许（TC-31）。
	if len(rule.WritableFields) == 0 {
		return fail(c, http.StatusConflict, codeReadOnly, "该台账为只读同步存档表，无写入口")
	}
	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的记录 id")
	}
	cond := permission.RowFilter("a", rule.RowScope, idn)
	if allowed, err := d.archiveAllowed(ctx, cond, id); err != nil || !allowed {
		return fail(c, http.StatusForbidden, codeRowForbidden, "越权访问：该记录不在你的可见范围内")
	}
	a, err := d.DB.GetArchiveByID(ctx, id)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "记录不存在")
	}

	var req struct {
		Fields map[string]any `json:"fields"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if len(req.Fields) == 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "fields 不能为空")
	}

	ops := d.loadOps(ctx, table, a.BizNo)
	for k, v := range req.Fields {
		if !permission.CanWrite(rule, k) {
			d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update", Resource: "ledger:" + table, TargetID: c.Param("id"), Result: "deny"})
			return fail(c, http.StatusForbidden, codeForbidden, "字段不可写: "+k)
		}
		ops[k] = v
	}
	raw, _ := json.Marshal(ops)
	if err := d.DB.UpsertOps(ctx, &store.LedgerOps{LedgerType: table, BizNo: a.BizNo, OpsJSON: string(raw), UpdatedBy: idn.OpenID}); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update", Resource: "ledger:" + table, TargetID: c.Param("id"), Result: "allow"})
	return ok(c, map[string]any{"ledger_type": table, "biz_no": a.BizNo, "ops": ops})
}

// ---------- 审计（M7） ----------

func (d Deps) handleAuditLogs(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	rule, _ := d.Perm.Resolve(ctx, "api:audit", idn)
	if permission.IsDenyAll(rule) {
		return fail(c, http.StatusForbidden, codeForbidden, "无权限查询审计日志")
	}
	_, size, offset := pageParams(c)
	rows, total, err := d.DB.ListAudit(ctx, store.AuditFilter{
		ActorOpenID: c.QueryParam("actor"),
		Role:        c.QueryParam("role"),
		Action:      c.QueryParam("action"),
		Resource:    c.QueryParam("resource"),
		Result:      c.QueryParam("result"),
		Limit:       size,
		Offset:      offset,
	})
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		items = append(items, map[string]any{
			"actor_open_id": a.ActorOpenID,
			"actor_role":    a.ActorRole,
			"action":        a.Action,
			"resource":      a.Resource,
			"target_id":     a.TargetID,
			"result":        a.Result,
			"feishu_log_id": a.FeishuLogID,
			"created_at":    a.CreatedAt.Format(time.RFC3339),
		})
	}
	return ok(c, map[string]any{"items": items, "total": total, "page_size": size, "offset": offset})
}

// ---------- 内部工具 ----------

// instanceAllowed 单条按 ID 访问同样施加行过滤（TC-06：直接按 ID 请求他部门记录必须被拒）。
func (d Deps) instanceAllowed(ctx context.Context, rule permission.Rule, idn permission.Identity, instanceCode string) (bool, error) {
	cond := instanceRowCondition(rule, idn)
	var n int
	q := `SELECT COUNT(*) FROM t_instance WHERE instance_code = ? AND (` + cond.SQL + `)`
	args := append([]any{instanceCode}, cond.Args...)
	if err := d.DB.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// archiveAllowed 台账单条按 ID 访问的行过滤校验（TC-06）。
func (d Deps) archiveAllowed(ctx context.Context, cond permission.Condition, id int64) (bool, error) {
	var n int
	q := `SELECT COUNT(*) FROM t_ledger_archive a WHERE a.id = ? AND (` + cond.SQL + `)`
	args := append([]any{id}, cond.Args...)
	if err := d.DB.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (d Deps) loadOps(ctx context.Context, ledgerType, bizNo string) map[string]any {
	out := map[string]any{}
	if o, err := d.DB.GetOps(ctx, ledgerType, bizNo); err == nil && strings.TrimSpace(o.OpsJSON) != "" {
		_ = json.Unmarshal([]byte(o.OpsJSON), &out)
	}
	return out
}

func (d Deps) formulaFlags(ctx context.Context, ledgerType string, a store.LedgerArchive) map[string]any {
	flags := map[string]any{
		"same_person":        false,
		"supplier_month_sum": nil,
		"spot_check_range":   false,
	}
	if strings.TrimSpace(a.ExtJSON) != "" {
		var ex map[string]any
		if json.Unmarshal([]byte(a.ExtJSON), &ex) == nil {
			if assigned, ok := ex["assigned_open_id"].(string); ok && assigned != "" && assigned == a.ApplicantOpenID {
				flags["same_person"] = true
			}
		}
	}
	if a.AmountCents != nil && d.Maps != nil {
		if lo, hi, ok := d.Maps.ThresholdRangeCents("spot_check_range"); ok {
			flags["spot_check_range"] = *a.AmountCents >= lo && *a.AmountCents <= hi
		}
	}
	if a.Supplier != "" && a.BizDate != "" && d.Maps != nil {
		prefix := strings.ReplaceAll(a.BizDate, "-", "")
		if len(prefix) > 4 {
			prefix = prefix[:4]
		}
		if sum, err := d.DB.SumArchiveAmountBySupplierMonth(ctx, ledgerType, a.Supplier, prefix); err == nil {
			over := false
			if th, ok := d.Maps.ThresholdCents("split_supplier_month"); ok {
				over = sum >= th
			}
			flags["supplier_month_sum"] = map[string]any{
				"supplier": a.Supplier, "month": prefix, "sum_cents": sum, "over_threshold": over,
			}
		}
	}
	return flags
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

func instanceRowMap(it store.Instance) map[string]any {
	row := map[string]any{
		"instance_code": it.InstanceCode,
		"approval_code": it.ApprovalCode, // ★ 具体值待确认（Q1）
		"doc_type":      it.DocType,
		"biz_no":        it.BizNo,
		"status":        it.Status,
		"status_raw":    it.StatusRaw,
		"department":    it.Department,
		"source":        it.Source,
		"created_at":    it.CreatedAt.Format(time.RFC3339),
		"updated_at":    it.UpdatedAt.Format(time.RFC3339),
	}
	row["applicant"] = map[string]any{"open_id": it.ApplicantOpenID, "name": it.ApplicantName}
	if it.AmountCents != nil {
		row["amount_cents"] = *it.AmountCents
		row["amount_display"] = formatCents(*it.AmountCents)
	}
	if it.BizNo != "" {
		row["biz_no_parts"] = map[string]any{"prefix": it.BizNoPrefix, "yymm": it.BizNoYYMM, "seq": it.BizNoSeq}
	}
	return row
}

func setSessionCookie(c echo.Context, value string, env *config.Env, maxAge int) {
	secure := true
	if env != nil && env.IsDev() {
		secure = false // 开发模式本机 http，避免 Secure Cookie 被浏览器丢弃
	}
	c.SetCookie(&http.Cookie{
		Name:     access.CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
