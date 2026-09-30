package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/submission"
)

// 本次新增资源（api:petty-cash / api:submission）的角色口径（PRD §4.1 / API §3.5·§3.6）：
//
//	综合运营主管 = 归口核心：备付金登记与核销、报送登记、凭证包导出、集团侧人工登记（读写）；
//	主管领导     = 备付金余额只读（审批前查看，做法 A）；
//	项目总经理   = 报送记录只读（ALL）。
//
// 说明：行·列权限矩阵（t_permission_rule）尚未包含这两个新资源，数据行落地前在此显式校验，
// 待资源枚举入库后可平滑迁移为配置驱动（不改接口行为）。
const (
	roleOpsSupervisor = "综合运营主管"
	roleDeptLead      = "主管领导"
	roleProjectGM     = "项目总经理"
)

// authorizeRole 解析会话身份并做角色鉴权：无会话 → 40101；角色不符 → 40300 且写审计留痕。
// 返回 ok=false 时已写出响应，调用方应直接 `return nil`。
func (d Deps) authorizeRole(c echo.Context, resource string, allowed ...string) (permission.Identity, bool) {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		_ = fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
		return idn, false
	}
	for _, r := range allowed {
		if strings.EqualFold(strings.TrimSpace(idn.Role), strings.TrimSpace(r)) {
			return idn, true
		}
	}
	d.audit(c.Request().Context(), &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "denied",
		Resource: resource, Result: "deny", DetailJSON: `{"reason":"role_not_allowed"}`,
	})
	_ = fail(c, http.StatusForbidden, codeForbidden, "无权限")
	return idn, false
}

// handlePettyCashBalance 备付金余额只读展示（FR-M1-04 / FR-M1-07）。
//
// ★ 只读：本接口没有任何写入能力；备付金不定额（有多少用多少、以实有金额为限），
// 余额以「月核销后实有金额」为唯一可外部核对的锚点。权限：综合运营主管 + 主管领导（只读）。
func (d Deps) handlePettyCashBalance(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:petty-cash", roleOpsSupervisor, roleDeptLead)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	period := strings.TrimSpace(c.QueryParam("period"))
	if period == "" {
		period = submission.CurrentPeriod(time.Now())
	} else if p, valid := submission.NormalizePeriod(period); valid {
		period = p
	} else {
		return fail(c, http.StatusBadRequest, codeBadRequest, "账期格式应为 YYYY-MM")
	}

	info, found, err := submission.NewRepo(d.DB).PettyCashClose(ctx, period)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	body := map[string]any{
		"period":         period,
		"issued_cents":   int64(0),
		"spent_cents":    int64(0),
		"balance_cents":  int64(0),
		"amount_display": submission.FormatCents(0),
		"as_of":          "",
		"found":          found,
	}
	if found {
		body["issued_cents"] = info.IssuedCents
		body["spent_cents"] = info.SpentCents
		body["balance_cents"] = info.BalanceCents
		body["amount_display"] = submission.FormatCents(info.BalanceCents)
		body["remark"] = info.Remark
		if !info.AsOf.IsZero() {
			body["as_of"] = info.AsOf.Format(time.RFC3339)
		}
	}

	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
		Resource: "api:petty-cash", TargetID: period, Result: "allow",
	})
	return ok(c, body)
}

// handlePettyCashReceipt 备付金签领登记（FR-M1-01 / FR-M1-05）。
//
// 时序口径：采一档为「先领款、后购买」，故签领记录先于支出；签领记录与付款凭据逐笔一一对应。
// 权限：综合运营主管（登记岗）。
func (d Deps) handlePettyCashReceipt(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:petty-cash", roleOpsSupervisor)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	var req struct {
		BizNo          string `json:"biz_no"`
		ReceiverOpenID string `json:"receiver_open_id"`
		ReceiverName   string `json:"receiver_name"`
		AmountCents    int64  `json:"amount_cents"`
		ReceivedDate   string `json:"received_date"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if req.AmountCents <= 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "签领金额必须为正整数（分）")
	}
	if !submission.ValidDate(req.ReceivedDate) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "签领日期格式应为 YYYY-MM-DD")
	}

	id, err := d.DB.InsertPettyCashReceipt(ctx, req.BizNo, "", req.ReceiverOpenID, req.ReceiverName,
		req.AmountCents, req.ReceivedDate, idn.OpenID)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "create",
		Resource: "api:petty-cash", Result: "allow",
	})
	return ok(c, map[string]any{"id": id, "amount_cents": req.AmountCents})
}

// handlePettyCashMonthlyClose 备付金月核销（FR-M1-07）。
//
// ★ 唯一账期一条：同一 period 重复核销 → 40900；余额以核销后实有金额为准（不强制 issued-spent）。
// 权限：综合运营主管。
func (d Deps) handlePettyCashMonthlyClose(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:petty-cash", roleOpsSupervisor)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	var req struct {
		Period       string `json:"period"`
		IssuedCents  int64  `json:"issued_cents"`
		SpentCents   int64  `json:"spent_cents"`
		BalanceCents int64  `json:"balance_cents"`
		Remark       string `json:"remark"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	period, valid := submission.NormalizePeriod(req.Period)
	if !valid {
		return fail(c, http.StatusBadRequest, codeBadRequest, "账期格式应为 YYYY-MM")
	}
	if req.IssuedCents < 0 || req.SpentCents < 0 || req.BalanceCents < 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "金额不得为负")
	}

	err := submission.NewRepo(d.DB).InsertPettyCashClose(ctx, period,
		req.IssuedCents, req.SpentCents, req.BalanceCents, req.Remark, idn.OpenID)
	if err != nil {
		if err == submission.ErrConflict {
			return fail(c, http.StatusConflict, codeConflict, "该账期已核销（唯一账期一条）")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "create",
		Resource: "api:petty-cash", TargetID: period, Result: "allow",
	})
	return ok(c, map[string]any{
		"period":         period,
		"issued_cents":   req.IssuedCents,
		"spent_cents":    req.SpentCents,
		"balance_cents":  req.BalanceCents,
		"amount_display": submission.FormatCents(req.BalanceCents),
		"remark":         req.Remark,
		// T1 参数消费：备付金报销时限（type=none ⇒ 随时报销、不设时限、零阻断）——
		// 与个人报销的 25 日截止**分列两套**（params.json#petty_cash.reimburse_deadline）。
		"reimburse_deadline": d.pettyCashDeadlinePolicy(),
	})
}
