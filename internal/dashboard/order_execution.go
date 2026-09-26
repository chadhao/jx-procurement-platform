package dashboard

import (
	"context"
	"math"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// L11 订单执行台账的**派生词表**（与 `GET /api/ledger/L11` 同口径）。
//
// ★ 为什么必须统一到这套键名（架构审查 **A-1b**）：
// 看板 14 原先按**中文运营键**（`实际到货` / `延期天数` / `完成日期`）读 L11，而派生视图产出的是
// `order_state` / `actual_arrival` / `delay_days` —— **两套互不兼容的语义**。
// L11 已是派生视图、**不会存在运营行**，故中文键在 L11 上永远不可能命中。
// 以派生词表为准，看板与台账视图从此同源。
const (
	keyOrderState    = "order_state"
	keyActualArrival = "actual_arrival"
	keyDelayDays     = "delay_days"
	keyDeliveryDue   = "delivery_due"
	// keyArrivalDate 合同侧的交期字段名（落在 ext_json，由 field_id→biz_field 映射得来）。
	keyArrivalDate = "delivery_date"
)

// orderExecutionRows 派生 L11 订单执行行（架构审查 **A-1** 修复）。
//
// 口径同 `internal/httpapi/handlers_ledger_derived.go`：以**合同台账（L04）为骨架**，
// 关联**到货验收（L07，GR 以 `ext_json.related_biz_no` 指向合同号）**派生。
//
// ★ 为什么必须改：看板 14「采购执行」原从 `t_ledger_archive` 读 `ledger_type='L11'`，
// 而 L11 **从不落行**（`config.DerivedLedgerTypes` 明示、导入层亦硬拦 `doc_type→L11`）——
// 于是「在途订单数 / 平均采购周期 / 延期订单 TOP5 / 月度采购金额趋势 / 同供应商当月累计 TOP」
// **五项指标在生产环境全部恒空或恒 0，且无任何日志与报错**。
// 这正是本项目反复出现的「静默」缺陷形态；改后看板与台账派生视图**共用同一口径**，不会再分叉。
//
// 行级过滤：骨架查询与关联查询**均注入** `q.LedgerRowSQL/Args`（别名 a），不漏过滤。
func (b *Builder) orderExecutionRows(ctx context.Context, windowStart string, q Query) ([]Row, error) {
	ctRows, err := b.fetchLedgerRows(ctx, []string{"L04"}, windowStart, q)
	if err != nil || len(ctRows) == 0 {
		return nil, err
	}

	bizNos := make([]string, 0, len(ctRows))
	for _, r := range ctRows {
		if r.BizNo != "" {
			bizNos = append(bizNos, r.BizNo)
		}
	}
	grByContract := map[string]store.LedgerArchive{}
	if len(bizNos) > 0 {
		linked, err := b.db.ListArchiveByExtKeyIn(ctx, "L07", store.KeyRelatedBizNo, bizNos,
			q.LedgerRowSQL, q.LedgerRowArgs)
		if err != nil {
			return nil, err
		}
		for _, gr := range linked {
			rel := gr.ExtString(store.KeyRelatedBizNo)
			if rel == "" {
				continue
			}
			// 同一合同关联多行 L07 时**优先取到货验收单（GR）**，避免被检验报告等顶掉到货字段。
			if prev, exist := grByContract[rel]; exist && isArrivalDoc(prev) {
				continue
			}
			if _, exist := grByContract[rel]; !exist || isArrivalDoc(gr) {
				grByContract[rel] = gr
			}
		}
	}

	out := make([]Row, 0, len(ctRows))
	for _, ct := range ctRows {
		row := ct
		row.LedgerType = "L11" // 语义上属订单执行台账（数据源＝CT + GR）
		ops := map[string]any{}
		if due, ok := ct.ArchiveExt[keyArrivalDate]; ok {
			ops[keyDeliveryDue] = due
		}
		if gr, ok := grByContract[ct.BizNo]; ok {
			ops[keyOrderState] = "已到货"
			if gr.BizDate != "" {
				ops[keyActualArrival] = gr.BizDate
			}
			if d1, ok1 := parseDayStrict(jsonStr(ops[keyDeliveryDue])); ok1 {
				if d2, ok2 := parseDayStrict(gr.BizDate); ok2 {
					ops[keyDelayDays] = int64(math.Round(d2.Sub(d1).Hours() / 24))
				}
			}
		} else {
			ops[keyOrderState] = "在途"
		}
		row.Ops = ops
		out = append(out, row)
	}
	return out, nil
}

// isArrivalDoc 判断该 L07 行是否为到货验收单（GR）。
func isArrivalDoc(a store.LedgerArchive) bool {
	return strings.EqualFold(strings.TrimSpace(a.SourceDocType), "GR")
}
