package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/submission"
)

// ---------- M6 报送（FR-M6-01 ~ FR-M6-08） ----------

// handleCreateSubmission 提交集团登记（FR-M6-01 / FR-M6-02 / FR-M6-05）。
//
//   - `Idempotency-Key` 头（docs/05-API.md §2.2 / §8）：**同键 + 同载荷 → 200 且响应体为首次登记结果**
//     （网络重试 / 重复点击的真实语义）；**同键 + 异载荷 → 40900**（真正的键复用冲突）；
//   - ★「无凭证视为未提交」：receipt_ref 为空时 submit_state 强制判为「未提交」（TC-15）；
//   - ★ `biz_no` **必填**：它是业务唯一键（台账关联与去重的锚点）。SQLite 的列级 `UNIQUE`
//     **对 NULL 不生效**（可插入任意多条 NULL），故允许空单号会使去重形同虚设（见 0002 迁移说明）；
//   - 关联单据清单、事项类型、金额、付款方式、湖南侧完成日期一并登记。
//
// 权限：综合运营主管。
func (d Deps) handleCreateSubmission(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:submission", roleOpsSupervisor)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	var req struct {
		BizNo        string `json:"biz_no"`
		SubjectType  string `json:"subject_type"`
		AmountCents  *int64 `json:"amount_cents"`
		PayMethod    string `json:"pay_method"`
		HNFinishDate string `json:"hn_finish_date"`
		SubmitDate   string `json:"submit_date"`
		ReceiptRef   string `json:"receipt_ref"`
		SubmitState  string `json:"submit_state"`
		// ★ Q14-B 第 5 项：行级权限按真实列重建所需的身份字段（均可选）。
		//   不传时对应行的四个 scope 令牌仍 fail-closed（1=0），不会放宽为全量。
		Department      string   `json:"department"`
		ApplicantOpenID string   `json:"applicant_open_id"`
		AssignedOpenID  string   `json:"assigned_open_id"`
		Acceptors       []string `json:"acceptors"`
		Items           []struct {
			ItemBizNo string `json:"item_biz_no"`
			ItemType  string `json:"item_type"`
		} `json:"items"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	req.BizNo = strings.TrimSpace(req.BizNo)
	if req.BizNo == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"业务单号不能为空（biz_no 为报送记录的业务唯一键，也是去重锚点）")
	}
	if strings.TrimSpace(req.SubjectType) == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "事项类型不能为空")
	}
	// ★ Q21 定案：单笔金额必须 **> 0**（0 与负数一律拒绝）。
	if req.AmountCents != nil && *req.AmountCents <= 0 {
		return fail(c, http.StatusBadRequest, codeBadRequest, "金额必须大于 0（Q21：不接受 0 元与负数）")
	}
	if req.HNFinishDate != "" && !submission.ValidDate(req.HNFinishDate) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "湖南侧完成日期格式应为 YYYY-MM-DD")
	}
	if req.SubmitDate != "" && !submission.ValidDate(req.SubmitDate) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "提交日期格式应为 YYYY-MM-DD")
	}
	if strings.TrimSpace(req.SubmitState) != "" && !submission.ValidState(req.SubmitState) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法报送状态: "+req.SubmitState)
	}

	// ★ 部门补全（B35）：请求未带 department 时，退用**登记人本人的部门**。
	//   否则 `DEPT` / `CHARGE_DEPT` 两个 row_scope 令牌在该报送上永远命中 0 条
	//   （列有值才谈得上按部门过滤）——属于“列存在但没有写入者”的静默失效。
	dept := strings.TrimSpace(req.Department)
	if dept == "" {
		if ur, err := d.DB.GetUserRole(ctx, idn.OpenID); err == nil && ur != nil {
			dept = strings.TrimSpace(ur.Department)
		}
	}

	// ★ 载荷指纹：同键同载荷 → 幂等复用；同键异载荷 → 40900。
	idemKey := strings.TrimSpace(c.Request().Header.Get("Idempotency-Key"))
	items := make([]store.SubmissionItem, 0, len(req.Items))
	payload := submission.IdemPayload{
		BizNo:        req.BizNo,
		SubjectType:  req.SubjectType,
		PayMethod:    req.PayMethod,
		HNFinishDate: req.HNFinishDate,
		SubmitDate:   req.SubmitDate,
		ReceiptRef:   req.ReceiptRef,
		SubmitState:  req.SubmitState,
		HasAmount:    req.AmountCents != nil,
		// 身份字段参与指纹：同一 biz_no 换了归属部门或验收人，属**不同载荷**，不应被判为幂等复用。
		Department:      dept,
		ApplicantOpenID: strings.TrimSpace(req.ApplicantOpenID),
		AssignedOpenID:  strings.TrimSpace(req.AssignedOpenID),
		Acceptors:       normaliseStringList(req.Acceptors),
	}
	if req.AmountCents != nil {
		payload.AmountCents = *req.AmountCents
	}
	for _, it := range req.Items {
		if strings.TrimSpace(it.ItemBizNo) == "" {
			continue // 与指纹口径一致：空单号的项不落库、也不参与指纹
		}
		payload.Items = append(payload.Items, submission.IdemItem{BizNo: it.ItemBizNo, Type: it.ItemType})
		items = append(items, store.SubmissionItem{ItemBizNo: it.ItemBizNo, ItemType: it.ItemType})
	}
	fingerprint := payload.Fingerprint()

	// ★ 事务内完成「建报送 + 建关联项 + 占位幂等键」，消除「先查后写」竞态（TOCTOU）：
	//   见 submission.Repo.CreateWithIdem 顶部注释与 migrations/0002_idem_unique.sql。
	state := submission.ComputeSubmitState(req.ReceiptRef, req.SubmitState)
	id, replayID, err := submission.NewRepo(d.DB).CreateWithIdem(ctx, &store.Submission{
		BizNo:        req.BizNo,
		SubjectType:  req.SubjectType,
		AmountCents:  req.AmountCents,
		PayMethod:    req.PayMethod,
		HNFinishDate: req.HNFinishDate,
		SubmitDate:   req.SubmitDate,
		ReceiptRef:   req.ReceiptRef,
		SubmitState:  state,
		// ★ 身份字段：写入真实列，供 row_scope 的 DEPT / CHARGE_DEPT / ASSIGNED / PARTICIPATED 使用。
		//   申请人未显式给出时，退用登记人本人（不猜测他人身份）。
		Department:      dept,
		ApplicantOpenID: firstNonEmptyStr(req.ApplicantOpenID, idn.OpenID),
		AssignedOpenID:  strings.TrimSpace(req.AssignedOpenID),
		Acceptors:       marshalStringList(req.Acceptors),
		CreatedBy:       idn.OpenID,
	}, items, idemKey, fingerprint, idn.OpenID)

	switch {
	case errors.Is(err, submission.ErrIdemReplay):
		// ① 同键 + 同载荷 → 返回首次登记结果（本次事务已整体回滚，未写入任何数据）。
		return d.replaySubmission(c, ctx, replayID, idn)
	case errors.Is(err, submission.ErrIdemConflict):
		// ② 同键 + 异载荷（或历史遗留无指纹行）→ 40900 幂等冲突。
		return fail(c, http.StatusConflict, codeConflict,
			"Idempotency-Key 冲突：该键已用于另一次请求（请求载荷不一致）")
	case errors.Is(err, submission.ErrDuplicateBizNo):
		return fail(c, http.StatusConflict, codeConflict, "业务单号已存在："+req.BizNo)
	case err != nil:
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "create",
		Resource: "api:submission", TargetID: req.BizNo, Result: "allow",
	})
	return ok(c, submissionCreateBody(id, req.BizNo, state, req.HNFinishDate, req.SubmitDate))
}

// replaySubmission 幂等复用分支：读取首次登记结果并按**同一响应形状**返回（200）。
//
// ★ 响应体与首次登记保持一致（同一个 4 字段载荷），仅额外标记 `idempotent_replay=true`，
// 以便调用方与日志侧区分「新登记」与「幂等复用」；本分支不新增 / 不修改任何数据。
func (d Deps) replaySubmission(c echo.Context, ctx context.Context, firstID int64,
	idn permission.Identity) error {
	first, err := d.DB.GetSubmission(ctx, firstID)
	if err != nil {
		if err == store.ErrNotFound {
			// 幂等键登记与报送记录不一致（数据被人工清理）：显式报错，不静默伪造结果。
			return fail(c, http.StatusConflict, codeConflict,
				"Idempotency-Key 已登记但对应报送记录不存在，请核查数据一致性")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "create",
		Resource: "api:submission", TargetID: first.BizNo, Result: "allow",
		DetailJSON: `{"idempotent_replay":true}`,
	})
	body := submissionCreateBody(first.ID, first.BizNo, first.SubmitState,
		first.HNFinishDate, first.SubmitDate)
	body["idempotent_replay"] = true
	return ok(c, body)
}

// submissionCreateBody 组装「报送登记」响应体（首次登记与幂等复用共用同一形状）。
func submissionCreateBody(id int64, bizNo, state, hnFinishDate, submitDate string) map[string]any {
	return map[string]any{
		"id":           id,
		"biz_no":       bizNo,
		"submit_state": state,
		"overdue":      submission.IsOverdue(hnFinishDate, submitDate, state, time.Now()),
	}
}

// handleListSubmissions 报送记录列表 / 月度「已提交未付款」对账清单（FR-M6-03 / FR-M6-04 / FR-M6-08）。
//
//	state：未提交 / 已提交 / 办理中 / 已付款 / 已驳回；period：YYYY-MM；overdue：true|false；分页。
//
// 响应含 overdue（3 个工作日超期）红标。权限：综合运营主管 + 项目总经理（只读）。
func (d Deps) handleListSubmissions(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:submission", roleOpsSupervisor, roleProjectGM)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	state := strings.TrimSpace(c.QueryParam("state"))
	if state != "" && !submission.ValidState(state) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法报送状态: "+state)
	}
	period := strings.TrimSpace(c.QueryParam("period"))
	if period != "" {
		if p, valid := submission.NormalizePeriod(period); valid {
			period = p
		} else {
			return fail(c, http.StatusBadRequest, codeBadRequest, "账期格式应为 YYYY-MM")
		}
	}

	rows, err := submission.NewRepo(d.DB).ListAllSubmissions(ctx, state, period)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	now := time.Now()
	items := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		subItems, err := d.DB.ListSubmissionItems(ctx, s.ID)
		if err != nil {
			return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		}
		items = append(items, submission.RecordMap(s, subItems, now))
	}

	if f := strings.TrimSpace(c.QueryParam("overdue")); f != "" {
		want := parseBoolLoose(f)
		filtered := items[:0]
		for _, it := range items {
			if it["overdue"] == want {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}

	page, size, _ := pageParams(c)
	total := len(items)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}

	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
		Resource: "api:submission", Result: "allow",
	})
	return ok(c, map[string]any{
		"items":     items[start:end],
		"total":     total,
		"page":      page,
		"page_size": size,
	})
}

// handleSubmissionPackage 报送凭证包导出（FR-M6-06 / FR-M7-03 / TC-26）。
//
// 导出内容：关联单据清单 + 移交凭证 + 湖南侧完成日期 + 付款方式等；导出行为写审计留痕（action='export'）。
// 权限：综合运营主管 / 项目总经理（读取）。format=zip|pdf。
func (d Deps) handleSubmissionPackage(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:submission", roleOpsSupervisor, roleProjectGM)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的报送 id")
	}
	format := strings.ToLower(strings.TrimSpace(c.QueryParam("format")))
	if format == "" {
		format = "zip"
	}
	if format != "zip" && format != "pdf" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "format 仅支持 zip|pdf")
	}

	sub, err := d.DB.GetSubmission(ctx, id)
	if err != nil {
		if err == store.ErrNotFound {
			return fail(c, http.StatusNotFound, codeNotFound, "报送记录不存在")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items, err := d.DB.ListSubmissionItems(ctx, id)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	// ★ 附件（B39 缺口②）：按**关联单据的业务单号**取附件元数据纳入凭证包 ——
	//   否则集团收到的是「只有清单没有文件」的空包。读不到只记 warn、不使打包失败
	//   （凭证包本身仍有效），但**必须留痕**。
	bizNos := make([]string, 0, len(items))
	for _, it := range items {
		bizNos = append(bizNos, it.ItemBizNo)
	}
	attsRows, err := d.DB.ListAttachmentsByBizNos(ctx, bizNos)
	if err != nil {
		d.Log.Warn("凭证包：附件清单读取失败，本次包内附件清单为空", "submission_id", id, "error", err.Error())
	}
	atts := make([]submission.PackageAttachment, 0, len(attsRows))
	for _, a := range attsRows {
		var size int64
		if a.SizeBytes != nil {
			size = *a.SizeBytes
		}
		atts = append(atts, submission.PackageAttachment{
			FileID: a.FileID, FileName: a.FileName, BizNo: a.BizNo,
			Fetched: a.StorageKey != "", SizeBytes: size,
		})
	}

	// ★ 导出留痕（TC-26）：先写审计，再返回文件流。
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "export",
		Resource: "submission_package", TargetID: c.Param("id"), Result: "allow",
		DetailJSON: `{"format":"` + format + `"}`,
	})

	now := time.Now()
	if format == "pdf" {
		data := submission.BuildPackagePDF(*sub, items, atts, now)
		c.Response().Header().Set(echo.HeaderContentDisposition,
			`attachment; filename="submission-`+c.Param("id")+`.pdf"`)
		return c.Blob(http.StatusOK, "application/pdf", data)
	}
	data, err := submission.BuildPackageZip(*sub, items, atts, now)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	c.Response().Header().Set(echo.HeaderContentDisposition,
		`attachment; filename="submission-`+c.Param("id")+`.zip"`)
	return c.Blob(http.StatusOK, "application/zip", data)
}

// handleRegisterSubmissionReceipt 移交凭证（签收记录）登记 / 补填（FR-M6-02 / FR-M6-05）。
//
// ★ 补填后「未提交」自动转为「已提交」（TC-15 步骤 3）。权限：综合运营主管。
func (d Deps) handleRegisterSubmissionReceipt(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:submission", roleOpsSupervisor)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的报送 id")
	}
	sub, err := d.DB.GetSubmission(ctx, id)
	if err != nil {
		if err == store.ErrNotFound {
			return fail(c, http.StatusNotFound, codeNotFound, "报送记录不存在")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	var req struct {
		ReceiptRef string `json:"receipt_ref"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	ref := strings.TrimSpace(req.ReceiptRef)
	if ref == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "移交凭证号不能为空")
	}

	newState := sub.SubmitState
	if !submission.Submitted(sub.SubmitState) {
		newState = submission.StateSubmitted
	}
	hit, err := submission.NewRepo(d.DB).ApplyReceipt(ctx, id, ref, newState)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	if !hit {
		return fail(c, http.StatusNotFound, codeNotFound, "报送记录不存在")
	}

	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update",
		Resource: "api:submission", TargetID: c.Param("id"), Result: "allow",
	})
	return ok(c, map[string]any{
		"id":           id,
		"receipt_ref":  ref,
		"submit_state": submission.ComputeSubmitState(ref, newState),
	})
}

// handleRegisterSubmissionGroup 集团侧字段人工登记（FR-M6-07）。
//
// ★ 仅作人工登记、不回填、不作数据对齐：受理编号 / 流程状态 / 付款完成日期均取人工传入值，
// 不做任何派生；请求状态为「已提交」但无移交凭证 → 40000（无凭证视为未提交）。
// 权限：综合运营主管。
func (d Deps) handleRegisterSubmissionGroup(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:submission", roleOpsSupervisor)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的报送 id")
	}
	sub, err := d.DB.GetSubmission(ctx, id)
	if err != nil {
		if err == store.ErrNotFound {
			return fail(c, http.StatusNotFound, codeNotFound, "报送记录不存在")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	var req struct {
		GrpAcceptNo string `json:"grp_accept_no"`
		GrpState    string `json:"grp_state"`
		PaidDate    string `json:"paid_date"`
		SubmitState string `json:"submit_state"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if req.PaidDate != "" && !submission.ValidDate(req.PaidDate) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "付款完成日期格式应为 YYYY-MM-DD")
	}
	newState := ""
	if strings.TrimSpace(req.SubmitState) != "" {
		if !submission.ValidState(req.SubmitState) {
			return fail(c, http.StatusBadRequest, codeBadRequest, "非法报送状态: "+req.SubmitState)
		}
		// ★ 除了「未提交」，任何状态都蕴含「已提交集团」，故必须先有移交凭证。
		//
		// 原实现只拦「已提交」，于是「已付款 / 已驳回 / 办理中」在无凭证时会被写入库，
		// 而读取路径的 ComputeSubmitState 又按「无凭证视为未提交」把它盖回「未提交」——
		// 结果是**状态被静默丢弃**：写进去是「已付款」，查出来是「未提交」，
		// 用户与对账都很可能据此误判。此处改为**显式报错**，宁可拒绝也不留不一致。
		if req.SubmitState != submission.StateUnsubmitted && strings.TrimSpace(sub.ReceiptRef) == "" {
			return fail(c, http.StatusBadRequest, codeBadRequest,
				"无移交凭证，不得登记为「"+req.SubmitState+"」（无凭证视为未提交；请先用 /receipt 登记移交凭证）")
		}
		newState = req.SubmitState
	}

	hit, err := submission.NewRepo(d.DB).ApplyGroup(ctx, id, req.GrpAcceptNo, req.GrpState, req.PaidDate, newState)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	if !hit {
		return fail(c, http.StatusNotFound, codeNotFound, "报送记录不存在")
	}

	updated, err := d.DB.GetSubmission(ctx, id)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update",
		Resource: "api:submission", TargetID: c.Param("id"), Result: "allow",
	})
	return ok(c, submission.RecordMap(*updated, nil, time.Now()))
}

// handleRejectSubmission 集团驳回处置登记（FR-M6-08）。
//
// 处置方式二择一：取消 / 驳回重走，须填原因；登记后状态置为「已驳回」并在台账标注原因。
// 权限：综合运营主管。
func (d Deps) handleRejectSubmission(c echo.Context) error {
	idn, allowed := d.authorizeRole(c, "api:submission", roleOpsSupervisor)
	if !allowed {
		return nil
	}
	ctx := c.Request().Context()

	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的报送 id")
	}
	sub, err := d.DB.GetSubmission(ctx, id)
	if err != nil {
		if err == store.ErrNotFound {
			return fail(c, http.StatusNotFound, codeNotFound, "报送记录不存在")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}

	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if !submission.ValidRejectAction(req.Action) {
		return fail(c, http.StatusBadRequest, codeBadRequest, "处置方式须为「取消」或「驳回重走」")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "驳回原因不能为空")
	}
	// ★ 同 /group：登记「已驳回」意味着该笔确实提交过集团，故必须有移交凭证，
	// 否则会被读取路径的 ComputeSubmitState 盖回「未提交」→ 静默丢失驳回结论。
	if strings.TrimSpace(sub.ReceiptRef) == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"无移交凭证，不得登记驳回处置（无凭证视为未提交；请先用 /receipt 登记移交凭证）")
	}
	reason := strings.TrimSpace(req.Action) + "：" + strings.TrimSpace(req.Reason)

	hit, err := submission.NewRepo(d.DB).ApplyReject(ctx, id, reason, submission.StateRejected)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	if !hit {
		return fail(c, http.StatusNotFound, codeNotFound, "报送记录不存在")
	}

	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "update",
		Resource: "api:submission", TargetID: c.Param("id"), Result: "allow",
	})
	return ok(c, map[string]any{
		"id":            id,
		"submit_state":  submission.StateRejected,
		"reject_action": strings.TrimSpace(req.Action),
		"reject_reason": reason,
	})
}

// parseBoolLoose 宽松解析布尔查询参数（true/1/yes/y 为真）。
func parseBoolLoose(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "y":
		return true
	default:
		return false
	}
}
