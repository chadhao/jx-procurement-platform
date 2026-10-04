// dateRange —— N-054 ① · date_range（iso_interval 载荷契约）纯函数模块。
// ★ 纯 ESM：不 import Vue、不碰 DOM（Node 直跑验收）。
// 契约＝spec/chain.json#conventions.field_payload_forms：
//   `YYYY-MM-DD/YYYY-MM-DD`（恰好一个斜杠、两段合法 ISO 日期、闭区间 start ≤ end）。

/** 解析 iso_interval 串 → { start, end, error }。
 *  合法 ⇒ error=''；非法 ⇒ { start:'', end:'', error:人读提示 }（不清空调用方数据）。 */
export function parseIsoInterval(v) {
  const s = String(v == null ? '' : v).trim()
  if (s === '') return { start: '', end: '', error: '' }
  const parts = s.split('/')
  if (parts.length !== 2) {
    return { start: '', end: '', error: `既有值形态非法（${s}）：须为 YYYY-MM-DD/YYYY-MM-DD` }
  }
  const start = parts[0].trim()
  const end = parts[1].trim()
  const re = /^\d{4}-\d{2}-\d{2}$/
  if (!re.test(start) || !re.test(end)) {
    return { start: '', end: '', error: `既有值形态非法（${s}）：两段须为 YYYY-MM-DD` }
  }
  if (start > end) {
    return { start: '', end: '', error: `既有值起止倒置（${s}）：起不得晚于止` }
  }
  return { start, end, error: '' }
}

/** 组装 iso_interval 串（提交载荷形态）→ { value, error }。
 *  两段齐且合法 ⇒ value='YYYY-MM-DD/YYYY-MM-DD'；任一为空 ⇒ value=''（提交时不带该字段）；
 *  起 > 止 ⇒ error 可见（不得提交）。 */
export function buildIsoInterval(start, end) {
  const s = String(start == null ? '' : start).trim()
  const e = String(end == null ? '' : end).trim()
  if (s === '' && e === '') return { value: '', error: '' }
  if (s === '' || e === '') return { value: '', error: '发生期间须同时填写起、止日期' }
  const re = /^\d{4}-\d{2}-\d{2}$/
  if (!re.test(s) || !re.test(e)) return { value: '', error: '发生期间日期格式须为 YYYY-MM-DD' }
  if (s > e) return { value: '', error: '发生期间起始日期不得晚于结束日期' }
  return { value: `${s}/${e}`, error: '' }
}
