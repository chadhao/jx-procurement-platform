package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 本文件实现 Q14 定稿后的两处台账读侧口径（2026-09-26）：
//
//	① L07 到货验收台账：**GR 与 QC 各写一行 + 查询侧关联**（Q14-B 第 1 项，建议①）
//	   —— QC（来料检验报告）以其 `ext_json.related_biz_no` 指向 GR（到货验收单）；
//	     读 GR 行时把 QC 的检验结论附加到该行，QC 自身仍作为 L07 的独立一行可查。
//	② L11 订单执行台账：**派生视图、不落行**（Q14-B 第 2 项，建议①）
//	   —— 数据源＝CT（L04 合同台账）+ GR（L07 到货验收台账），查询时聚合；
//	     L11 不接受实例级落账（导入层已拒绝映射到 L11），也没有写入口。

// linkedInspectionKey 关联块在台账行里的键名（标量 + 单层对象，随行的列投影一并裁剪）。
const linkedInspectionKey = "inspection"

// attachLinkedInspection 为 L07 行附加关联 QC 的检验结论（就地修改 row）。
//
// ★ 只对台账 L07 生效；其他台账原样返回。
// ★ 关联方向：QC 行 → `related_biz_no` = GR 单号。故：
//   - 读 **GR** 行时，附上指向它的 QC 的检验结论（`inspection`）；
//   - 读 **QC** 行时，附上它指向的 GR 单号（`inspection.linked_biz_no` 反向提示），便于双向追溯。
func attachLinkedInspection(row map[string]any, a store.LedgerArchive, qcByRelated map[string]store.LedgerArchive) {
	if a.LedgerType != "L07" {
		return
	}
	if strings.EqualFold(strings.TrimSpace(a.SourceDocType), "QC") {
		// QC 自身：给出它关联的 GR，便于从检验报告跳到验收单。
		if related := a.ExtString(store.KeyRelatedBizNo); related != "" {
			row[linkedInspectionKey] = map[string]any{
				"linked_biz_no": related,
				"source":        "QC",
			}
		}
		return
	}
	// GR（或 L07 内其他单据）：附上指向它的 QC 结论。
	if qc, ok := qcByRelated[a.BizNo]; ok {
		blk := map[string]any{
			"biz_no":        qc.BizNo,
			"linked_biz_no": a.BizNo,
			"source":        "QC",
		}
		if v := qc.ExtString(config.BizFieldInspectionResult); v != "" {
			blk["result"] = v
		}
		if v := qc.ExtString("inspection_item"); v != "" {
			blk["item"] = v
		}
		if v := qc.ExtString("inspection_standard"); v != "" {
			blk["standard"] = v
		}
		if !qc.UpdatedAt.IsZero() {
			blk["updated_at"] = qc.UpdatedAt
		}
		row[linkedInspectionKey] = blk
	}
}

// loadLinkedInspections 批量取「指向本页各行」的 QC 行，避免逐行 N+1（Q14-B 第 1 项查询侧实现）。
// 返回 map[GR 单号]QC 行。
func (d Deps) loadLinkedInspections(
	ctx context.Context, rows []store.LedgerArchive, rowCond permission.Condition,
) map[string]store.LedgerArchive {
	if len(rows) == 0 {
		return nil
	}
	bizNos := make([]string, 0, len(rows))
	for _, a := range rows {
		if strings.EqualFold(strings.TrimSpace(a.SourceDocType), "QC") {
			continue // QC 不接收"指向自己的 QC"
		}
		if a.BizNo != "" {
			bizNos = append(bizNos, a.BizNo)
		}
	}
	if len(bizNos) == 0 {
		return nil
	}
	// 注意：关联查询同样施加**行级过滤** —— 否则会把别的部门的检验结论挂到本部门行上。
	linked, err := d.DB.ListArchiveByExtKeyIn(ctx, "L07", store.KeyRelatedBizNo, bizNos, rowCond.SQL, rowCond.Args)
	if err != nil {
		// 关联块是增强信息，取不到不应使整页失败；但要留痕（不静默）。
		d.Log.Warn("L07 关联 QC 查询失败，检验结论将不出现在本页", "error", err.Error())
		return nil
	}
	out := make(map[string]store.LedgerArchive, len(linked))
	for _, qc := range linked {
		if !strings.EqualFold(strings.TrimSpace(qc.SourceDocType), "QC") {
			continue
		}
		if rel := qc.ExtString(store.KeyRelatedBizNo); rel != "" {
			// 同一 GR 可能有多份检验报告：保留较早的一份，避免结果随写入顺序抖动。
			if _, exist := out[rel]; !exist {
				out[rel] = qc
			}
		}
	}
	return out
}

// ---------- L11 订单执行台账：派生视图（不落行） ----------

// handleLedgerDerivedList GET /api/ledger/L11 —— 派生行：由 L04（CT）+ L07（GR）聚合。
//
// 派生行**无 `id`**，故不可 PATCH；写入口另由 handleLedgerPatch 显式拒绝。
func (d Deps) handleLedgerDerivedList(c echo.Context, idn permission.Identity, rule permission.Rule) error {
	ctx := c.Request().Context()
	cond := permission.RowFilter("a", rule.RowScope, idn)
	sensitive, _ := d.DB.SensitiveFields(ctx, c.Param("table"))
	page, size, offset := pageParams(c)

	// ① 以合同台账（L04）为骨架分页（订单执行以合同/简式订单为单位）。
	contracts, total, err := d.DB.ListArchive(ctx, store.LedgerFilter{
		LedgerType: "L04",
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

	// ② 批量取这些合同关联的到货验收单（L07/GR），同样施加行级过滤。
	bizNos := make([]string, 0, len(contracts))
	for _, ct := range contracts {
		if ct.BizNo != "" {
			bizNos = append(bizNos, ct.BizNo)
		}
	}
	grByContract := map[string]store.LedgerArchive{}
	if len(bizNos) > 0 {
		linked, err := d.DB.ListArchiveByExtKeyIn(ctx, "L07", store.KeyRelatedBizNo, bizNos, cond.SQL, cond.Args)
		if err != nil {
			d.Log.Warn("L11 派生：关联到货验收单查询失败，到货字段将为空", "error", err.Error())
		}
		for _, gr := range linked {
			rel := gr.ExtString(store.KeyRelatedBizNo)
			if rel == "" {
				continue
			}
			// 同一合同若关联到多行 L07（正常只有一张 GR；若操作方把别的单据类型也指向合同号则会多），
			// **优先取到货验收单（GR）**，避免被 QC 等行顶掉 `actual_arrival`。
			if prev, exist := grByContract[rel]; exist && isArrivalDoc(prev) {
				continue
			}
			if _, exist := grByContract[rel]; !exist || isArrivalDoc(gr) {
				grByContract[rel] = gr
			}
		}
	}

	items := make([]map[string]any, 0, len(contracts))
	for _, ct := range contracts {
		row := orderExecutionRow(ct, grByContract[ct.BizNo])
		items = append(items, permission.ProjectDeep(row, rule.ColumnAllow, rule.ColumnDeny, sensitive))
	}
	d.audit(ctx, &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
		Resource: "ledger:" + c.Param("table"), Result: "allow",
		DetailJSON: `{"derived":true}`,
	})
	return ok(c, map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": size,
		"derived":   true,
		"source":    []string{"L04", "L07"},
	})
}

// isArrivalDoc 判断该 L07 行是否为到货验收单（GR）——用于在同一合同的多个关联行中优先取它。
func isArrivalDoc(a store.LedgerArchive) bool {
	return strings.EqualFold(strings.TrimSpace(a.SourceDocType), "GR")
}

// orderExecutionRow 由「合同行 + 到货验收行」派生一行订单执行。
//
// 字段口径（架构 §3.4 第 11 号）：合同号、供应商、金额、交期、实际到货、延期天数、订单状态。
func orderExecutionRow(ct, gr store.LedgerArchive) map[string]any {
	row := map[string]any{
		"derived":       true,
		"ledger_type":   "L11",
		"biz_no":        ct.BizNo, // 订单执行以合同/简式订单号为单位（PO 不作独立单据）
		"contract_no":   ct.BizNo,
		"department":    ct.Department,
		"applicant":     ct.ApplicantOpenID,
		"supplier":      ct.Supplier,
		"instance_code": ct.InstanceCode,
		"biz_date":      ct.BizDate,
		"source":        "L04+L07",
	}
	if ct.AmountCents != nil {
		row["amount_cents"] = *ct.AmountCents
		row["amount_display"] = formatCents(*ct.AmountCents)
	}
	if due := ct.ExtString("delivery_date"); due != "" {
		row["delivery_due"] = due
	}
	if gr.BizNo == "" {
		row["order_state"] = "在途"
		return row
	}
	row["gr_biz_no"] = gr.BizNo
	if gr.BizDate != "" {
		row["actual_arrival"] = gr.BizDate
	}
	row["order_state"] = "已到货"
	// 延期天数：交期与实际到货都有且为 YYYY-MM-DD 时才计算；否则字段不出现（不臆造 0）。
	if d1, ok1 := parseISODate(row["delivery_due"]); ok1 {
		if d2, ok2 := parseISODate(row["actual_arrival"]); ok2 {
			row["delay_days"] = int64(d2.Sub(d1).Hours() / 24)
		}
	}
	return row
}
