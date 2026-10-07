package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/jsonutil"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ---------- 免登与会话 ----------

// handleFeishuCallback 飞书免登回调；未映射角色的 open_id 拒绝进入业务页（TC-32）。
// 开发模式（DEV_MODE=true）允许以 ?open_id= 直接建立会话，用于无凭据的端到端验证。
//
// ★ state 真校验：官方《获取授权码》要求「务必校验 state 前后一致」—— 下发时存于
//
//	HttpOnly Cookie（jx_oauth_state，见 handlers_auth.go），回调时比对，不一致 ⇒ 400 拒绝。
//
// ★ error=access_denied 分支：用户在授权页拒绝授权时官方回调
//
//	`<redirect_uri>?error=access_denied&state=<原值>`，友好跳登录页（不 500）。
//
// ★ 登录后回跳原目标页：authorize-url 把 ?redirect= 存入 HttpOnly Cookie
// （jx_oauth_redirect），成功建会话后取出并 302 过去；取不到 ⇒ 回落 /。
// DEV_MODE 直连路径无 Cookie 时额外接受 ?redirect=（本地验证用），同样过白名单。
func (d Deps) handleFeishuCallback(c echo.Context) error {
	ctx := c.Request().Context()
	code := c.QueryParam("code")
	state := c.QueryParam("state")
	devOpenID := c.QueryParam("open_id")

	// ★ 用户拒绝授权（官方失败回调形态）：友好提示，不当免登失败 500。
	if strings.TrimSpace(c.QueryParam("error")) != "" {
		return c.Redirect(http.StatusFound, "/login?error=denied")
	}

	// ★ DEV_MODE 的 ?open_id= 直连路径保持原语义（现有测试与 Login.vue 开发入口依赖）。
	devDirect := d.Auth.DevMode() && strings.TrimSpace(devOpenID) != ""

	if strings.TrimSpace(state) == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "缺少 state（防 CSRF）")
	}
	if !devDirect {
		// ★ state 前后一致性真校验：须与 authorize-url 下发并存入 Cookie 的值一致。
		sent := readOAuthStateCookie(c)
		clearOAuthStateCookie(c, d.Env) // 一次性：用毕即清，防重放
		if sent == "" || !oauthStateMatch(state, sent) {
			return fail(c, http.StatusBadRequest, codeBadRequest,
				"state 校验失败（防 CSRF），请重新发起登录")
		}
		if strings.TrimSpace(code) == "" {
			return fail(c, http.StatusBadRequest, codeBadRequest, "缺少 code")
		}
	}

	ident, err := d.Auth.Exchange(ctx, code, devOpenID)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeUnauthorized, "免登失败: "+err.Error())
	}

	ur, err := d.Auth.ResolveRole(ctx, ident.OpenID)
	if err != nil {
		// ★ N-075：未映射审批角色 ≠ 必拒 —— 先查系统角色（t_sys_role）：
		//   仅系统角色者（Q3 初始管理员只种 t_sys_role）也须能登录进管理域；
		//   两者皆无 ⇒ 维持默认拒绝（deny by default 不变）。
		sysRoles, serr := d.DB.GetSysRoles(ctx, ident.OpenID)
		if serr != nil || len(sysRoles) == 0 {
			// ★ 默认拒绝：未映射角色不得进入业务页。
			d.audit(ctx, &store.AuditLogRow{ActorOpenID: ident.OpenID, Action: "login",
				Resource: "auth", Result: "deny", DetailJSON: `{"reason":"role_not_mapped"}`})
			return fail(c, http.StatusUnauthorized, codeRoleMapped, "未配置角色，请联系系统管理员")
		}
		value := d.Auth.Establish(ident.OpenID)
		setSessionCookie(c, value, d.Env, int((8 * time.Hour).Seconds()))
		d.audit(ctx, &store.AuditLogRow{ActorOpenID: ident.OpenID, ActorRole: strings.Join(sysRoles, ","),
			Action: "login", Resource: "auth", Result: "allow"})
		return c.Redirect(http.StatusFound, d.postLoginTarget(c))
	}

	value := d.Auth.Establish(ident.OpenID)
	setSessionCookie(c, value, d.Env, int((8 * time.Hour).Seconds()))
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: ident.OpenID, ActorRole: ur.Role, Action: "login", Resource: "auth", Result: "allow"})
	return c.Redirect(http.StatusFound, d.postLoginTarget(c))
}

// postLoginTarget 取「登录后回跳目标」（仅在会话建立成功后调用，取毕即清 Cookie）。
//
// 目标来源（按优先级）：
//  1. authorize-url 存入的 HttpOnly Cookie（jx_oauth_redirect，与 state 同批下发、
//     同一登录事务绑定 —— 首选）；
//  2. DEV_MODE 直连路径（?open_id=）无该 Cookie，额外接受 ?redirect= 查询参数
//     （仅本地验证用）。
//
// ★ 开放重定向防护（纵深防御）：取出的值**再次**过 sanitizeInternalRedirect 白名单
// 校验（写入侧已校验过一次；Cookie 可能被客户端篡改，取出后必须再验一次），
// 任一来源不合法 ⇒ 一律回落 /，绝不外跳。
func (d Deps) postLoginTarget(c echo.Context) string {
	target, ok := sanitizeInternalRedirect(readOAuthRedirectCookie(c))
	clearOAuthRedirectCookie(c, d.Env)
	if ok {
		return target
	}
	// Cookie 无效/不存在：DEV_MODE 直连路径兜底接受 ?redirect=（同样过白名单）。
	devOpenID := c.QueryParam("open_id")
	if d.Auth.DevMode() && strings.TrimSpace(devOpenID) != "" {
		if t, ok2 := sanitizeInternalRedirect(c.QueryParam("redirect")); ok2 {
			return t
		}
	}
	return "/"
}

// clearOAuthRedirectCookie 用毕即清（防陈旧目标跨会话复用；未用到的 10 分钟自然过期）。
func clearOAuthRedirectCookie(c echo.Context, env *config.Env) {
	setOAuthRedirectCookie(c, "", env)
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

// handleInstancePrefill GET /api/instances/:code/prefill?keys=a,b
//
// 关联单预填（B6 · CT#usage_category 从已批准 PR 带入）：从实例 ext_json 取
// 指定键的值 —— ★ 键白名单由 **spec 驱动**（全表单 `source=="system"` 字段），
// 再排除四个**非 related 带入型**键（其生命周期各自独立，不是从关联单来的）：
//
//	biz_no（提交后生成）· related_biz_no（用户输入源本身）·
//	purchaser / approval_record_ref（审批时点产生）。
//
// ★ 越权复用 instanceAllowed（与 detail 同口径）；键不在白名单 ⇒ 40000（不泄漏任意 ext 键）。
func (d Deps) handleInstancePrefill(c echo.Context) error {
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
	inst, err := d.DB.GetInstance(ctx, code)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "实例不存在")
	}
	// 键白名单：spec 全表单 source=system 字段（减去 related 带入的排除集）
	exclude := map[string]bool{
		"biz_no": true, "related_biz_no": true, "purchaser": true, "approval_record_ref": true,
	}
	allowedKeys := map[string]bool{}
	for _, form := range d.Spec.Forms {
		for _, sec := range form.Sections {
			for _, f := range sec.Fields {
				if f.Source == "system" && !exclude[f.Name] {
					allowedKeys[f.Name] = true
				}
			}
		}
	}
	requested := strings.Split(c.QueryParam("keys"), ",")
	if len(requested) == 0 || c.QueryParam("keys") == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "keys 不能为空（逗号分隔）")
	}
	ext := map[string]any{}
	if strings.TrimSpace(inst.ExtJSON) != "" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &ext); err != nil {
			return fail(c, http.StatusInternalServerError, codeInternal, "实例 ext_json 损坏")
		}
	}
	out := map[string]any{}
	for _, k := range requested {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !allowedKeys[k] {
			// ★ 白名单外不回显（防探测任意 ext 键 —— 同「字段级不泄漏」口径）
			return fail(c, http.StatusBadRequest, codeBadRequest, "键不在可预填白名单内（须为 spec 中 source=system 的关联带入型字段）: "+k)
		}
		if v, ok := ext[k]; ok && v != nil {
			out[k] = v
		}
	}
	return ok(c, map[string]any{"prefill": out})
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
	// 派生台账（L11 订单执行台账）走查询侧聚合，不读 t_ledger_archive 的 L11 行（Q14-B 第 2 项）。
	if config.IsDerivedLedger(table) {
		return d.handleLedgerDerivedList(c, idn, rule)
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
	// L07 到货验收台账：批量取关联的 QC 检验结论（各写一行 + 查询侧关联，Q14-B 第 1 项）。
	var qcByRelated map[string]store.LedgerArchive
	if table == "L07" {
		qcByRelated = d.loadLinkedInspections(ctx, rows, cond)
	}
	for _, a := range rows {
		// 读路径：单行运营字段坏掉不应让整页 500 → 降级为空，但**必须留痕**（不静默）。
		ops, opsErr := d.loadOps(ctx, table, a.BizNo)
		if opsErr != nil {
			d.Log.Warn("运营字段解析失败，本行按空处理（数据已损坏，请检查写入方）",
				"ledger_type", table, "biz_no", a.BizNo, "error", opsErr.Error())
			ops = map[string]any{}
		}
		row := ledgerRowMap(a, ops, d.formulaFlags(ctx, table, a))
		attachLinkedInspection(row, a, qcByRelated)
		// ★ 列级投影在序列化阶段裁剪，无权限字段连字段名都不出现（TC-07）；
		//   用递归版，使嵌套块（formula_flags / archive / inspection）同样受 deny 与敏感列约束（B19）。
		row = permission.ProjectDeep(row, rule.ColumnAllow, rule.ColumnDeny, sensitive)
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
	// 派生台账的行在查询时聚合产生、没有稳定主键，故不支持按 id 读取。
	if config.IsDerivedLedger(table) {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"该台账为派生视图（行由查询时聚合产生），不支持按 id 读取，请用列表接口")
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
	ops, opsErr := d.loadOps(ctx, table, a.BizNo)
	if opsErr != nil {
		d.Log.Warn("运营字段解析失败，本行按空处理（数据已损坏，请检查写入方）",
			"ledger_type", table, "biz_no", a.BizNo, "error", opsErr.Error())
		ops = map[string]any{}
	}
	row := ledgerRowMap(*a, ops, d.formulaFlags(ctx, table, *a))
	if table == "L07" {
		attachLinkedInspection(row, *a, d.loadLinkedInspections(ctx, []store.LedgerArchive{*a}, cond))
	}
	row = permission.ProjectDeep(row, rule.ColumnAllow, rule.ColumnDeny, sensitive)
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
	// ★ 派生视图 / 只读汇总 / 本期未启用：一律无写入口（Q14 定稿 2026-09-26）。
	if config.IsReadOnlyLedger(table) {
		return fail(c, http.StatusConflict, codeReadOnly,
			"该台账无写入口（派生视图 / 只读汇总 / 本期未启用）")
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
	// ★ 台账必须与 URL 一致（B36）：否则可用某台账的 id 往 ops 写一条挂在**别的台账**名下的记录，
	//   而读侧按 (ledger_type, biz_no) 取 → 写进去了却读不到（错位数据）。
	if a.LedgerType != table {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"记录不属于该台账：URL="+table+"，记录="+a.LedgerType)
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

	// ★ 写路径：**必须**拒绝解析失败，绝不能"当空继续"（静默审计 C6）。
	//   下面的逻辑是「读既有 ops → 合入本次 fields → **整体写回**」。若把损坏的
	//   ops_json 当成空对象，本次写回就会把**该行既有的全部运营字段抹掉**，
	//   而接口仍返回 200 —— 一次静默的数据丢失。
	ops, opsErr := d.loadOps(ctx, table, a.BizNo)
	if opsErr != nil {
		d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update",
			Resource: "ledger:" + table, TargetID: c.Param("id"), Result: "deny",
			DetailJSON: `{"reason":"ops_json_unparsable"}`})
		return fail(c, http.StatusInternalServerError, codeInternal,
			"该行既有运营字段无法解析，为免覆盖丢失已拒绝写入（请先修复 ops_json）: "+opsErr.Error())
	}
	// 台账字段定义白名单（Q14-B 第 4 项）：该台账**登记过**字段定义时，只接受登记过的键名，
	// 使 t_ledger_field_def 具备消费端（不再是"配了没人读"的空表）；未登记则不做键名限制（向后兼容）。
	declared, err := d.DB.LedgerFieldKeys(ctx, table)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	// ★ 键名比较与 `permission.CanWrite` 同口径（TrimSpace + 忽略大小写），
	//   否则会出现「白名单命中、字段定义未命中」的不一致（B34）。
	for k, v := range req.Fields {
		if !permission.CanWrite(rule, k) {
			d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update", Resource: "ledger:" + table, TargetID: c.Param("id"), Result: "deny"})
			return fail(c, http.StatusForbidden, codeForbidden, "字段不可写: "+k)
		}
		// ★ 仅当**该台账登记过字段定义**、且这是一个**未登记的新键**时才拒绝（B34）。
		//   行内既有的历史键（登记字段定义之前就写进去的）予以放行 —— 否则前端“整行 ops 回写”
		//   会因携带一个遗留键而**整行写不进去**（把无关字段一起挡住，代价远大于收益）。
		if len(declared) > 0 && !declared[strings.ToLower(strings.TrimSpace(k))] {
			if _, existed := ops[k]; !existed {
				d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update", Resource: "ledger:" + table, TargetID: c.Param("id"), Result: "deny"})
				return fail(c, http.StatusConflict, codeReadOnly, "字段未在台账字段定义中登记: "+k)
			}
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
	// ★ N-075（第 5 点 auditRoles ⇒ Role ∪ SysRoles 的运行期落点）：审计查询 =
	//   权限矩阵 api:audit 行（审批角色半边，如项目总经理）∪ 系统角色半边
	//   （SysRoles 含系统管理员 —— 系统角色不进矩阵，只能在此放行，见 Q2）。
	if permission.IsDenyAll(rule) && !hasSysRole(idn, roleSysAdmin) {
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
			"detail_json":   a.DetailJSON, // 改前/改后 diff（TC-35）
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
	q := `SELECT COUNT(*) FROM t_instance a WHERE instance_code = ? AND (` + cond.SQL + `)`
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

func (d Deps) loadOps(ctx context.Context, ledgerType, bizNo string) (map[string]any, error) {
	o, err := d.DB.GetOps(ctx, ledgerType, bizNo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	// ★ 不再 `_ = json.Unmarshal(...)`（静默审计 C6）：非法 JSON 会**返回错误**而不是
	//   静默留空 —— 读路径据此降级并留痕，写路径据此拒绝（见各调用点）。
	return jsonutil.Object(o.OpsJSON)
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
		// ★ 月份口径统一为 `YYYY-MM`（B32）：biz_date 全系统统一 `YYYY-MM-DD`，
		//   取前 7 位即月份。原实现把连字符去掉再截前 4 位 → 得到 `"2026"`（整年），
		//   与 `biz_date` 的存法对不上，「同供应商当月累计」实际统计的是**整年**。
		month := monthKey(a.BizDate)
		if sum, err := d.DB.SumArchiveAmountBySupplierMonth(ctx, ledgerType, a.Supplier, month); err == nil {
			over := false
			if th, ok := d.Maps.ThresholdCents("split_supplier_month"); ok {
				over = sum >= th
			}
			flags["supplier_month_sum"] = map[string]any{
				"supplier": a.Supplier, "month": month, "sum_cents": sum, "over_threshold": over,
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
