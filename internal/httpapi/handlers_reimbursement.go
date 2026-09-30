package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/submission"
)

// 集团报销跟踪表（M1，FR-M1-02）——「审批外人工登记」。
//
// ★ 口径（warranted by 五条前提第 ④ 条「报销类可提前支出、公户转账一律走集团」）：
// 报销**不进入本办法审批流程**，由人工走集团；本表只承接「关联事前申请单号 → 移交 → 集团付款」
// 的登记与查询，供台账（L06 集团提交与付款衔接台账）与看板引用。
//
// 与 M6 报送（t_submission）的区别：
//   - t_submission  = 湖南侧流程完成后的**对外报送**（含移交凭证、集团受理、驳回处置）；
//   - t_expense_track = **报销类事前申请**的单独跟踪（票据张数 / 初审状态 / 超支说明）。
//
// 权限：写（登记 / 更新集团侧字段）＝综合运营主管；读＝综合运营主管、主管领导、项目总经理、系统管理员。
// 集团侧付款字段（paid_date / paid_cents）**仅人工登记、不做任何派生回填**（FR-M6-07 同源口径）。

// reimbursableReviewStates 初审状态枚举（工具表·集团报销跟踪表口径；空 = 未填）。
var reimbursableReviewStates = map[string]bool{
	"待初审": true, "初审通过": true, "初审退回": true, "已移交集团": true, "集团已付款": true,
}

func validReviewState(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	return reimbursableReviewStates[s]
}

// handleCreateReimbursement POST /api/reimbursement（FR-M1-02）。
func (d Deps) handleCreateReimbursement(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:reimbursement", roleOpsSupervisor)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	var req struct {
		SrcBizNo        string `json:"src_biz_no"`
		ApplicantOpenID string `json:"applicant_open_id"`
		Department      string `json:"department"`
		ActualCents     int64  `json:"actual_cents"`
		InvoiceCount    int    `json:"invoice_count"`
		ReviewState     string `json:"review_state"`
		HandoverDate    string `json:"handover_date"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if strings.TrimSpace(req.SrcBizNo) == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"关联事前申请单号不能为空（src_biz_no 为报销跟踪的业务关联键）")
	}
	if req.ActualCents <= 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "实际金额必须为正整数（分）")
	}
	if req.InvoiceCount < 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "票据张数不得为负")
	}
	if req.HandoverDate != "" && !submission.ValidDate(req.HandoverDate) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "移交集团日期格式应为 YYYY-MM-DD")
	}
	if !validReviewState(req.ReviewState) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法初审状态: "+req.ReviewState)
	}

	id, err := d.DB.InsertExpenseTrack(ctx, req.SrcBizNo, req.ApplicantOpenID, req.Department,
		req.ActualCents, req.InvoiceCount, req.ReviewState, req.HandoverDate)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "create",
		Resource: "api:reimbursement", TargetID: strings.TrimSpace(req.SrcBizNo), Result: "allow",
		DetailJSON: `{"actual_cents":` + strconv.FormatInt(req.ActualCents, 10) + `,"invoice_count":` + strconv.Itoa(req.InvoiceCount) + `}`,
	})
	// T1 参数消费：月度截止日归集 + 超期处置（pending 按建议值运行）——
	// 消费函数＝params_consumers.reimbursementReportingView（读 spec/params.json，不写死）。
	if d.Spec == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "参数登记册未装配")
	}
	return ok(c, map[string]any{
		"id":             id,
		"src_biz_no":     strings.TrimSpace(req.SrcBizNo),
		"actual_cents":   req.ActualCents,
		"amount_display": formatCents(req.ActualCents),
		"reporting":      d.reimbursementReportingView(time.Now()),
	})
}

// handleListReimbursements GET /api/reimbursement（FR-M1-02）。
func (d Deps) handleListReimbursements(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:reimbursement",
		roleOpsSupervisor, roleDeptLead, roleProjectGM, roleSysAdmin)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()
	page, size, offset := pageParams(c)

	rows, total, err := d.DB.ListExpenseTracks(ctx, store.ExpenseTrackFilter{
		Department:  c.QueryParam("department"),
		ReviewState: c.QueryParam("review_state"),
		SrcBizNo:    c.QueryParam("src_biz_no"),
		Limit:       size,
		Offset:      offset,
	})
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	items := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		items = append(items, expenseTrackMap(e))
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
		Resource: "api:reimbursement", Result: "allow",
	})
	// T1 参数消费：「连续跨 2 个自然月未报 ⇒ 提示综合运营主管」——
	// 批次按 cutoff 归集后与当前批次比对（读 spec/params.json，不写死 25）。
	nudge := map[string]any{"over_2_months_count": 0}
	if d.Spec != nil {
		cutoff, _ := d.Spec.ParamInt("reporting.monthly_cutoff_day")
		cur, _ := batchPeriod(time.Now(), cutoff)
		n := 0
		for _, e := range rows {
			p, _ := batchPeriod(e.CreatedAt, cutoff)
			if monthsBetween(p, cur) >= 2 {
				n++
			}
		}
		nudge["over_2_months_count"] = n
		nudge["cutoff_day"] = cutoff
		if n > 0 {
			nudge["hint"] = fmt.Sprintf("有 %d 笔登记的报销已连续跨 2 个自然月未闭环，请综合运营主管确认", n)
		}
	}
	return ok(c, map[string]any{"items": items, "total": total, "page": page, "page_size": size, "nudge": nudge})
}

// monthsBetween 计算 YYYY-MM 批次之间的自然月差（a→b）；解析失败返回 0。
func monthsBetween(a, b string) int {
	var ay, am, by, bm int
	if _, err := fmt.Sscanf(a, "%04d-%02d", &ay, &am); err != nil {
		return 0
	}
	if _, err := fmt.Sscanf(b, "%04d-%02d", &by, &bm); err != nil {
		return 0
	}
	return (by-ay)*12 + (bm - am)
}

// handlePatchReimbursement PATCH /api/reimbursement/:id（FR-M1-02）。
//
// ★ 集团侧字段（paid_date / paid_cents）只接受人工传入值，**不做任何派生 / 回填**（FR-M6-07 同源）。
func (d Deps) handlePatchReimbursement(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:reimbursement", roleOpsSupervisor)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()
	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的记录 id")
	}

	var req struct {
		ReviewState  *string `json:"review_state"`
		HandoverDate *string `json:"handover_date"`
		PaidDate     *string `json:"paid_date"`
		PaidCents    *int64  `json:"paid_cents"`
		OverrunNote  *string `json:"overrun_note"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if req.ReviewState == nil && req.HandoverDate == nil && req.PaidDate == nil &&
		req.PaidCents == nil && req.OverrunNote == nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "至少提供一个待更新字段")
	}
	if req.ReviewState != nil && !validReviewState(*req.ReviewState) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法初审状态: "+*req.ReviewState)
	}
	if req.HandoverDate != nil && strings.TrimSpace(*req.HandoverDate) != "" && !submission.ValidDate(*req.HandoverDate) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "移交集团日期格式应为 YYYY-MM-DD")
	}
	if req.PaidDate != nil && strings.TrimSpace(*req.PaidDate) != "" && !submission.ValidDate(*req.PaidDate) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "集团付款日期格式应为 YYYY-MM-DD")
	}
	if req.PaidCents != nil && *req.PaidCents < 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "集团付款金额不得为负")
	}

	hit, err := d.DB.UpdateExpenseTrack(ctx, id, store.ExpenseTrackUpdate{
		ReviewState:  req.ReviewState,
		HandoverDate: req.HandoverDate,
		PaidDate:     req.PaidDate,
		PaidCents:    req.PaidCents,
		OverrunNote:  req.OverrunNote,
	})
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	if !hit {
		return fail(c, http.StatusNotFound, codeNotFound, "记录不存在")
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update",
		Resource: "api:reimbursement", TargetID: c.Param("id"), Result: "allow",
	})
	return ok(c, map[string]any{"id": id, "updated": true})
}

// expenseTrackMap 组装集团报销跟踪行（金额出参同时给 cents 与展示串）。
func expenseTrackMap(e store.ExpenseTrack) map[string]any {
	row := map[string]any{
		"id":            e.ID,
		"src_biz_no":    e.SrcBizNo,
		"applicant":     e.ApplicantOpenID,
		"department":    e.Department,
		"invoice_count": 0,
		"review_state":  e.ReviewState,
		"handover_date": e.HandoverDate,
		"paid_date":     e.PaidDate,
		"overrun_note":  e.OverrunNote,
		"created_by":    e.CreatedBy,
		"created_at":    e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"source":        "人工登记",
	}
	if e.ActualCents != nil {
		row["actual_cents"] = *e.ActualCents
		row["amount_display"] = formatCents(*e.ActualCents)
	}
	if e.InvoiceCount != nil {
		row["invoice_count"] = *e.InvoiceCount
	}
	if e.PaidCents != nil {
		row["paid_cents"] = *e.PaidCents
		row["paid_display"] = formatCents(*e.PaidCents)
	}
	return row
}
