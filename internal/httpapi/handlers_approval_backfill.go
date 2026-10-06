package httpapi

// SA 后置补录入口 `POST /api/approval/{biz_no}/backfill`（N-062 族 J3 · SA 侧 ·
// MIMO-NEXT-BATCH-27）—— 承载 `when=backfill(settlement_backfill)` 时点。
//
// ★ 契约正本 ＝ spec/chain.json#conventions.checks_when 第 ② 条（四道约束）：
//   ☆1 实例必须 APPROVED 且 doc_type 与段所属单据一致（SA）；
//   ☆2 只接受该段 `source=user` 的字段（白名单**从 spec/forms/SA.json 读**，不硬编码）；
//   ☆3 写入落 `t_instance.ext_json`（不新增表/列）——**键级合并**，不得整列覆盖
//     （否则会冲掉 applicant_department 等既有键，N-064 同族教训）；
//   ☆4 写后执行该段 `when=backfill(...)` 的 hard 判据，失败按 else 处置（可见拒绝）。
//
// ★ 两个时点的分工（批 42 口径的直接应用，MIMO-NEXT-BATCH-27 §T3）：
//   `approval(<node_id>)` ⇒ 通用求值器（handlers_approval_approvalchecks.go）；
//   `backfill(<section_id>)` ⇒ **本入口**承载 —— 两者互不并入。
//
// ★ 与 evaluateHardChecks（提交期）的关系：该函数绑定 *approvalSubmitBody 且 when 白名单
//   只收 submit ⇒ 形态上不可复用 ⇒ 本文件提供**最小专用引擎**（按 backfill(<sid>) 分派）。
//
// ★ 判定与写入的次序（对契约「写毕⇒同请求内跑判据」的实现取舍）：先在内存完成键级合并、
//   对**合并后的最终态**执行判据，**全部通过才落库一次** —— 与「写后判、失败回滚」的可观测
//   行为一致（失败 ⇒ 非 200 且**零写入**），且无半程写/回滚舞步。
//
// ★ 错误码（复用既有，不新增）：401 会话缺失 · 404 实例不存在 · 400 doc_type 不符 /
//   白名单外键 / 判据不过（点名判据）· 409 实例未到 APPROVED（状态未到，同 approval 家族
//   IllegalTransition→409 口径）· 403 非申请人本人（同 ErrNotAssignee→403 行级口径）·
//   500 ext_json 损坏 / 落库失败 / 规格未装配。

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/labstack/echo/v4"
)

// backfillSectionID SA 结算补录段（契约中的 <section_id>；doc_type 门已锁 SA）。
const backfillSectionID = "settlement_backfill"

type approvalBackfillBody struct {
	Fields map[string]any `json:"fields"`
}

// backfillCheckResult 响应 checks[] 的单条执行记录（只记**真执行**的判据；
// 因输入缺失而按口径跳过的不列入 —— 不把「没判」写成「passed」）。
type backfillCheckResult struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Passed   bool   `json:"passed"`
}

// handleApprovalBackfill 后置补录入口（api 组 ⇒ requireSession；行级＝申请人本人）。
func (d Deps) handleApprovalBackfill(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	if d.Spec == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "机读规格未装配")
	}
	ctx := c.Request().Context()
	bizNo := c.Param("biz_no")

	inst, err := d.DB.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fail(c, http.StatusNotFound, codeNotFound, "实例不存在")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	// ☆1a doc_type：本入口只承载 SA 的 settlement_backfill 段。
	if inst.DocType != "SA" {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"本入口仅承载 SA 的 settlement_backfill 段（doc_type 不符）")
	}
	// ☆1b 状态：后置补录只对**已批准**的事前申请单开放。
	if inst.Status != flow.InstanceApproved {
		return fail(c, http.StatusConflict, codeApprovalConflict,
			"实例未到 APPROVED，后置补录仅对已批准的事前申请单开放")
	}
	// 行级（沿用 approve 的行级口径）：本期取**申请人本人**（MIMO-NEXT-BATCH-27 §1.2；
	// 是否放宽到经办/运营由我方裁定 —— 本批不自行放宽）。
	if strings.TrimSpace(inst.ApplicantOpenID) != idn.OpenID {
		return fail(c, http.StatusForbidden, codeRowForbidden, "仅申请单申请人本人可补录")
	}

	var body approvalBackfillBody
	if _, err := decodeOptionalBody(c, &body); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	if body.Fields == nil {
		body.Fields = map[string]any{}
	}

	// ☆2 白名单：**从 spec 读**该段 source=user 的字段名（不硬编码 —— 防第二份真相）。
	sec := backfillSection(d.Spec, inst.DocType, backfillSectionID)
	if sec == nil {
		return fail(c, http.StatusInternalServerError, codeInternal,
			"机读规格缺 sections[id="+backfillSectionID+"]（声明写错必须可见失败）")
	}
	whitelist := map[string]bool{}
	for _, f := range sec.Fields {
		if f.Source == "user" {
			whitelist[f.Name] = true
		}
	}
	written := make([]string, 0, len(body.Fields))
	for k := range body.Fields {
		if !whitelist[k] {
			return fail(c, http.StatusBadRequest, codeBadRequest,
				"字段 "+k+" 不在 "+backfillSectionID+" 白名单（仅接受该段 source=user 字段）")
		}
		written = append(written, k)
	}
	sort.Strings(written) // 响应稳定序

	// ☆3 键级合并（N-064 口径：损坏 ext ⇒ 可见失败，绝不静默抹掉其它键）。
	ext := map[string]any{}
	if strings.TrimSpace(inst.ExtJSON) != "" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &ext); err != nil {
			return fail(c, http.StatusInternalServerError, codeInternal,
				"实例 ext_json 损坏（拒绝补录，避免静默抹掉其它键）: "+err.Error())
		}
	}
	for k, v := range body.Fields {
		ext[k] = v
	}

	// ☆4 对**合并后终态**执行 when=backfill(settlement_backfill) 的 hard 判据；
	// 全过 ⇒ 一次落库（失败 ⇒ 非 200 且零写入）。
	checks, verr := d.evaluateBackfillChecks(inst, ext)
	if verr != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, verr.Error())
	}
	merged, err := json.Marshal(ext)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	inst.ExtJSON = string(merged)
	inst.UpdatedAt = time.Now()
	inst.UpdateTime++
	if err := d.DB.UpsertInstance(ctx, inst); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return ok(c, map[string]any{
		"biz_no":     inst.BizNo,
		"section_id": backfillSectionID,
		"written":    written,
		"checks":     checks,
	})
}

// backfillSection 从 spec 取段（找不到 ⇒ nil，调用方可见失败）。
func backfillSection(spec *specload.Bundle, docType, sectionID string) *specload.SectionDoc {
	if spec == nil {
		return nil
	}
	form, ok := spec.Forms[docType]
	if !ok {
		return nil
	}
	for i := range form.Sections {
		if form.Sections[i].ID == sectionID {
			return &form.Sections[i]
		}
	}
	return nil
}

// backfillCheckFn 单条 backfill hard 判据：返回 (是否真执行, 错误)。
// ran=false ＝ 按口径跳过（如 actual_cents 未填 ⇒ 「未填 ≠ 0」不代入判据，spec rule 明文）。
type backfillCheckFn func(inst *store.Instance, merged map[string]any) (bool, error)

// backfillCheckFns 注册表（与 submitHardChecks/approvalCheckFns 同族，按 id 索引）。
var backfillCheckFns = map[string]backfillCheckFn{
	"actual_not_exceed": checkBackfillActualNotExceed,
	"invoice_must_link": checkBackfillInvoiceMustLink,
}

// evaluateBackfillChecks 遍历 form.checks 中 when==backfill(<section>) 且 hard 的判据。
// 未注册 ⇒ 可见失败（与 submit/approval 引擎同款「声明了没执行」口径）。
func (d Deps) evaluateBackfillChecks(inst *store.Instance, merged map[string]any) ([]backfillCheckResult, error) {
	form, ok := d.Spec.Forms[inst.DocType]
	if !ok {
		return nil, nil
	}
	want := "backfill(" + backfillSectionID + ")"
	var results []backfillCheckResult
	for _, ck := range form.Checks {
		if ck.Severity != "hard" || strings.TrimSpace(ck.When) != want {
			continue
		}
		fn, ok := backfillCheckFns[ck.ID]
		if !ok {
			return nil, errors.New("backfill 判据 " + ck.ID + " 未注册求值器 —— 声明了没执行，可见失败（请在 backfillCheckFns 注册）")
		}
		ran, err := fn(inst, merged)
		if err != nil {
			return nil, err
		}
		if ran {
			results = append(results, backfillCheckResult{ID: ck.ID, Severity: "hard", Passed: true})
		}
	}
	return results, nil
}

// checkBackfillActualNotExceed SA#actual_not_exceed：actual_cents <= 批准额
// （approved ≡ 实例 amount_cents，即 header 段 amount_cents 落规范列）。
// ★ 未填 ≠ 0（spec rule 明文）：本次与历史都未给 actual_cents ⇒ 跳过不判（ran=false）。
func checkBackfillActualNotExceed(inst *store.Instance, merged map[string]any) (bool, error) {
	raw, has := merged["actual_cents"]
	if !has {
		return false, nil
	}
	amt, ok := toFloat64(raw)
	if !ok {
		if s, isStr := raw.(string); isStr {
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return true, errors.New("actual_cents 须为整数分（SA#actual_not_exceed 输入不可判）")
			}
			amt = f
		} else {
			return true, errors.New("actual_cents 须为整数分（SA#actual_not_exceed 输入不可判）")
		}
	}
	if inst.AmountCents == nil {
		return true, errors.New("实例批准额（amount_cents）缺失，无法判定 SA#actual_not_exceed")
	}
	if int64(amt) > *inst.AmountCents {
		return true, errors.New("实际金额 " + strconv.FormatInt(int64(amt), 10) +
			" 分超过批准额 " + strconv.FormatInt(*inst.AmountCents, 10) +
			" 分 —— SA#actual_not_exceed：须走超支确认，未确认不得移交集团")
	}
	return true, nil
}

// checkBackfillInvoiceMustLink SA#invoice_must_link：票据关联到已批准的事前申请单
// ⇒ 机判形态＝invoice_info 非空（「已批准」由入口 ☆1 保证；关联载体＝invoice_info
// 的票据引用，spec 该字段 required:true）。
func checkBackfillInvoiceMustLink(_ *store.Instance, merged map[string]any) (bool, error) {
	if strings.TrimSpace(strField(merged, "invoice_info")) == "" {
		return true, errors.New("票据未关联到已批准的事前申请单（invoice_info 为空）—— SA#invoice_must_link：不予受理")
	}
	return true, nil
}
