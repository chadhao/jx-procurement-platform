// permissionColumnList.js —— 权限矩阵三列（列白/黑名单、可写字段）「可编辑列表」纯函数
// （零依赖、node 直测；同 permissionRuleFilter.js / sysRole.js 的做法）。
//
// ★ 动因（用户原话）：「可写字段里边内容也太多了，做成一个可编辑列表。另外，我都不清楚
//   黑名单、白名单和可写字段允许的值分别都有什么，且每个是什么含义，很难填这些空。」
//   ⇒ 三列由逗号分隔文本框改为「chip 可编辑列表」；合法值＋中文含义的唯一来源＝
//     GET /api/admin/permission-column-catalog（后端已实现，数据源＝运行时 specload.Forms
//     ＋ 既有规则取值 —— 不新增 spec 文件，避免第二份真相）。
//
// ★★ 保存格式不变：buildSavePayload 产出的载荷与改前**逐字段同构**（仍是
//   { resource, role, row_scope, column_allow: string[], column_deny: string[], writable_fields: string[] }）
//   —— 可编辑列表只是**编辑形态**，不是新契约；后端 PUT /api/admin/permission-rules 零变化
//   （spec.mjs 里有「新旧构造等价」断言钉住这一点）。
//
// ★★ 两套命名（用户痛点之一，chipParts/含义块都要让两处都看到中文）：
//   · 列黑/白名单（column_deny/column_allow）取值＝**英文键**（amount_cents 等）；
//   · 可写字段（writable_fields）取值＝**中文列名**（「经办状态」等）。
//   catalog 对两者都给中文标签（可写列自标签），故 chip 一律「中文 label 优先、英文 key 次级」。

/** catalogByKey —— items[] → Map<key, item>（chip 文案/下拉/提示的查找索引）。 */
export function catalogByKey(catalog) {
  const m = new Map()
  for (const it of catalog || []) {
    if (it && it.key) m.set(it.key, it)
  }
  return m
}

/**
 * chipParts —— 单个取值的 chip 展示要素。
 * @returns {{key:string, label:string, known:boolean, conflict:string[]}}
 *   label：中文标签优先（无标签/未登记 ⇒ 原值）；conflict：同名不同标签的「表单：标签」清单
 *   （后端 label_conflict 如实列出首个之外的全部，chip 的 title 提示用，不静默挑一个）。
 */
export function chipParts(value, byKey) {
  const key = String(value == null ? '' : value)
  const it = byKey && typeof byKey.get === 'function' ? byKey.get(key) : undefined
  if (!it) return { key, label: key, known: false, conflict: [] }
  const label = (it.label || '').trim() || key
  const conflict = (it.label_conflict || []).map((c) => `${c.form}：${c.label}`)
  return { key, label, known: true, conflict }
}

/**
 * parseManualInput —— 手动输入解析（与改前 Admin.vue#splitList **同口径**：
 * 按 ASCII 逗号/中文逗号/换行切分、trim、去空）。保口径⇒粘贴旧格式文本行为不变。
 */
export function parseManualInput(text) {
  return String(text == null ? '' : text)
    .split(/[,，\n]/)
    .map((s) => s.trim())
    .filter(Boolean)
}

/** addValues —— 去重追加（保序：原值在前，新值按出现序附后；空值忽略）。 */
export function addValues(list, values) {
  const out = Array.isArray(list) ? list.slice() : []
  const seen = new Set(out)
  for (const v of values || []) {
    const s = String(v == null ? '' : v).trim()
    if (!s || seen.has(s)) continue
    seen.add(s)
    out.push(s)
  }
  return out
}

/** removeValue —— 删除单个取值（无则原样返回新数组）。 */
export function removeValue(list, value) {
  return (Array.isArray(list) ? list : []).filter((v) => v !== value)
}

/**
 * catalogOptions —— 下拉选项：catalog 全序、**排除已选**（防重复添加）。
 * 不排序、不改写 —— 目录序由后端保证（按 key 排序），前端不再造第二序。
 */
export function catalogOptions(catalog, selected) {
  const sel = new Set(selected || [])
  return (catalog || []).filter((it) => it && it.key && !sel.has(it.key))
}

/** optionText —— 下拉选项文案：中文 label 优先、英文 key 次级（与 chip 同构）。 */
export function optionText(item) {
  const key = (item && item.key) || ''
  const label = ((item && item.label) || '').trim()
  return label && label !== key ? `${label}（${key}）` : key
}

/**
 * manualStatus —— 手动输入框的实时合法值提示（用户：「很难填这些空」）。
 * @returns {{values:string[], known:string[], unknown:string[]}}
 *   unknown 非空 ⇒ 提示「不在目录中，仍可按原值保存」（后端不校验该列，目录是辅助不是约束）。
 */
export function manualStatus(text, byKey) {
  const values = parseManualInput(text)
  const known = []
  const unknown = []
  for (const v of values) {
    if (byKey && typeof byKey.has === 'function' && byKey.has(v)) known.push(v)
    else unknown.push(v)
  }
  return { values, known, unknown }
}

/**
 * buildSavePayload —— 保存载荷（与改前逐字段同构；见文件头「保存格式不变」）。
 * ★ 三列直接取数组（改前是 splitList(joinList(x)) 往返，等价性由 spec 钉住）。
 */
export function buildSavePayload(rules) {
  return (rules || []).map((r) => ({
    resource: r.resource,
    role: r.role,
    row_scope: r.row_scope,
    column_allow: (r._allow || []).map(String),
    column_deny: (r._deny || []).map(String),
    writable_fields: (r._writable || []).map(String),
  }))
}

// datalist 需唯一 id：计数器必须放在**模块作用域**（ES 模块单例）—— 放在 <script setup>
// 顶层会随每实例重置 ⇒ 一页多行组件全拿同一个 id（document 里 id 重复，list 指向第一个）。
let datalistSeq = 0

/** nextDatalistId —— 组件实例唯一 id（跨实例不重复；每实例各渲染自己的 datalist）。 */
export function nextDatalistId() {
  return `clist-datalist-${++datalistSeq}`
}
