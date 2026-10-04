// repeatRows —— N-050 · 重复段（明细行）纯函数模块。
// ★ 纯 ESM：不 import Vue、不碰 DOM（我方验收用 Node 直跑）。
// 契约：docs/05-API.md §4.6（form_errors）＋ N-040 裁定③（fields.<id>=行对象数组）。

/** 行内可编辑字段（等价 Submit.vue 原 rowEditableFields：只收 source==='user'）。 */
export function rowEditableFieldsOf(section) {
  return ((section && section.fields) || []).filter((f) => f.source === 'user')
}

/**
 * 批量粘贴解析（T3②）：
 *  - 按行切分，丢弃全空行；
 *  - 该行含 \t ⇒ 按 \t 切列；否则按 , 切列；
 *  - 列顺序 ＝ fields 声明顺序；列多于字段数 ⇒ error 可见（不静默丢弃）；列少 ⇒ 其余留空；
 *  - 值按「手工输入同等口径」原样存字符串（money 元、数字字符串 —— 换算只发生在
 *    buildRepeatingPayload 提交时，粘贴路径绝不再换算一次）。
 * 返回 { rows: 行对象数组, error: 人读错误串或 '' }。
 */
export function parseBulkRows(fields, text) {
  const rows = []
  const lines = String(text == null ? '' : text).split(/\r?\n/)
  for (let li = 0; li < lines.length; li++) {
    const line = lines[li]
    if (line.trim() === '') continue // 丢弃全空行
    const cols = line.includes('\t') ? line.split('\t') : line.split(',')
    if (cols.length > fields.length) {
      return {
        rows: [],
        error: `文本第 ${li + 1} 行有 ${cols.length} 列，多于该段字段数 ${fields.length}（不静默丢弃——请检查是否带了表头或多余分隔）`,
      }
    }
    const row = {}
    for (let i = 0; i < fields.length; i++) {
      row[fields[i].name] = i < cols.length ? cols[i] : ''
    }
    rows.push(row)
  }
  return { rows, error: '' }
}

/**
 * 行级错误定位（T2；契约 §4.6）：入参 ＝ data.form_errors 数组。
 * 返回 { first, keys }：
 *   - first ＝ 首个可定位项 { section_id, row_index, field_name }（无可定位行 ⇒ null）；
 *   - keys  ＝ 全数组定位键 `${section_id}|${row_index}|${field_name}`（★ 必须遍历全数组）。
 * 入参非数组 / 非对象元素 ⇒ { first: null, keys: [] }（不抛异常）。
 */
export function locateFormErrors(formErrors) {
  const keys = []
  let first = null
  if (!Array.isArray(formErrors)) return { first, keys }
  for (const fe of formErrors) {
    if (!fe || typeof fe !== 'object') continue
    const sid = fe.section_id == null ? '' : String(fe.section_id)
    const ri = Number(fe.row_index) || 0
    const fn = fe.field_name == null ? '' : String(fe.field_name)
    keys.push(`${sid}|${ri}|${fn}`)
    if (first === null && (ri > 0 || fn)) first = { section_id: sid, row_index: ri, field_name: fn }
  }
  return { first, keys }
}
