// ledgerTypes.js —— 台账类型语义键（L01..L12）。
//
// ★ 权威口径：工具表·台账与看板设计（12 张台账）与架构 §3.4。
// 后端 `GET /api/ledger/:table` 的 `:table` 值即此处的 `key`（`ledger_type`），
// 此前前端曾用 purchase / expense / petty_cash 等自造键，导致查询恒空（FR-M4-01），已纠正。
//
// 顺序与 PRD §5.5「12 张台账清单」一致，不得随意调整（看板与台账口径依赖该顺序）。

export const LEDGER_TYPES = [
  { key: 'L01', name: '采购备案台账', note: '付款凭据号 + 抽查状态属审批后字段（运营表）' },
  { key: 'L02', name: '采购需求与审批台账', note: '字段全部在审批内产生' },
  { key: 'L03', name: '采购经办登记台账', note: '指定经办人 / 指定时间 = 凭证；经办状态属审批后字段' },
  { key: 'L04', name: '合同台账', note: '履约状态、质保金余额属审批后字段；可查变更链' },
  { key: 'L05', name: '费用事前申请台账', note: '结算状态、移交集团日期属审批后字段' },
  { key: 'L06', name: '集团提交与付款衔接台账', note: '含「集团报销跟踪表」职能；集团侧字段全部人工登记' },
  { key: 'L07', name: '到货验收台账', note: '检验结论、差异说明由质检后续补充' },
  { key: 'L08', name: '供应商档案与绩效表', note: '手工维护表，本身可写' },
  { key: 'L09', name: '例外事项台账', note: '独家 / 紧急 / 变更；含「是否已核销闭合」' },
  { key: 'L10', name: '用途分类汇总台账', note: '只读，不做拆分' },
  { key: 'L11', name: '订单执行台账', note: '实际到货、延期天数属审批后字段' },
  { key: 'L12', name: '预算执行台账', note: '本期不启用，保留结构' },
]

/** 默认台账（与后端默认可查类型一致）。 */
export const DEFAULT_LEDGER_TYPE = 'L01'

/** 台账中文名（找不到时回落为 key 本身）。 */
export function ledgerName(key) {
  const t = LEDGER_TYPES.find((x) => x.key === key)
  return t ? t.name : key
}

// 无写入口的台账：派生视图（L11）/ 只读汇总（L10）/ 本期未启用（L12）。
// ★ 必须与后端 config.ReadOnlyLedgerTypes 保持一致——否则前端会显示一个必然 40901 的写按钮。
export const READ_ONLY_LEDGER_TYPES = ['L10', 'L11', 'L12']

export function isReadOnlyLedger(key) {
  return READ_ONLY_LEDGER_TYPES.includes(key)
}
