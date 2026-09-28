// 看板 chart key → 中文显示名映射（仅前端展示层；后端 key 是接口契约，不得改动）。
//
// ★ 维护约定：后端（internal/dashboard/*.go）新增 chart key 时，必须同步在本表补一行，
//   否则看板标题会兜底显示「其他指标」而不是具体名称。

/** @type {Record<string, string>} chart key → 中文标题 */
const CHART_LABELS = {
  delay_top5: '延期订单 TOP5',
  monthly_amount_trend: '月度金额趋势',
  top_supplier_month: '本月供应商 TOP',
  expense_by_department: '部门费用分布',
  expense_by_category: '费用类别分布',
  expense_by_supplier: '供应商费用分布',
  expense_monthly_trend: '费用月度趋势',
}

/**
 * 返回 chart key 的中文显示名。
 * 兜底：映射表未命中时返回「其他指标」，绝不把裸英文 key 直接展示给用户；
 * 调用方应将原始 key 放入 title 等属性，便于排查漏配。
 * @param {string} key 后端下发的 chart key
 * @returns {string} 中文显示名
 */
export function chartLabel(key) {
  return CHART_LABELS[key] || '其他指标'
}
