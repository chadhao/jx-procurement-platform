// permissionColumnList.spec.mjs —— 三列「可编辑列表」纯函数直跑（零依赖）。
// 运行：node web/src/permissionColumnList.spec.mjs
//
// ★ 夹具与生产同形：catalog items 形如 GET /api/admin/permission-column-catalog 的
//   { key, label, kinds, forms, label_conflict }（见 internal/httpapi/handlers_admin_column_catalog.go）；
//   规则行形如 Admin.vue#loadRules 的 { resource, role, row_scope, _allow, _deny, _writable }（★ 现为数组）。
// ★ 覆盖：① 手动输入解析与改前 splitList 同口径 ② 去重追加/删除 ③ chip 文案（中文优先·未登记原样·冲突清单）
//   ④ 下拉排除已选 ⑤ 实时合法值提示 ⑥ ★★ 保存载荷与改前**逐字段同构**（新旧构造等价，契约未变）。
//
// ★ 断言分组与命名：**单点变异**（buildSavePayload 把三列写成 joinList 字符串）应使
//   「⑥ 契约等价」两条恰红、其余保持绿 —— 故每条断言独立 ok()，不因一条失败而吞掉后续。
import {
  catalogByKey,
  chipParts,
  parseManualInput,
  addValues,
  removeValue,
  catalogOptions,
  optionText,
  manualStatus,
  buildSavePayload,
  nextDatalistId,
} from './permissionColumnList.js'

let failed = 0
function ok(cond, name) {
  if (cond) {
    console.log(`PASS ${name}`)
  } else {
    failed++
    console.log(`FAIL ${name}`)
  }
}

// ---- 夹具：与后端 catalog 响应同形（节选真实形态：英文键+中文标签/自标签/无标签三类）----
const CATALOG = [
  { key: 'amount_cents', label: '金额', kinds: ['money', 'data'], forms: ['BA', 'SA'], label_conflict: [{ form: 'SA', label: '申请金额' }] },
  { key: 'biz_no', label: '验收单号', kinds: ['data'], forms: ['GR'], label_conflict: [] },
  { key: 'member_purchaser', label: '采购经办人', kinds: ['data'], forms: ['GR'], label_conflict: [] },
  { key: 'formula_flags', label: '', kinds: ['money', 'ledger'], forms: [], label_conflict: [] },
  { key: 'grp_state', label: '', kinds: ['ledger'], forms: [], label_conflict: [] },
  // 可写字段：取值即中文列名（后端自标签：label === key）
  { key: '经办状态', label: '经办状态', kinds: ['writable'], forms: [], label_conflict: [] },
  { key: '检验结论', label: '检验结论', kinds: ['writable'], forms: [], label_conflict: [] },
]
const byKey = catalogByKey(CATALOG)
ok(byKey.size === CATALOG.length, `夹具：目录索引 ${CATALOG.length} 项`)

// ---- ① 手动输入解析：与改前 splitList 同口径（ASCII/中文逗号/换行、trim、去空）----
ok(JSON.stringify(parseManualInput('a, b，c\n d')) === JSON.stringify(['a', 'b', 'c', 'd']), '①解析：中英文逗号/换行混切')
ok(JSON.stringify(parseManualInput('  ,  ,')) === '[]', '①解析：纯分隔符 ⇒ 空数组')
ok(JSON.stringify(parseManualInput('金额, 经办状态')) === JSON.stringify(['金额', '经办状态']), '①解析：中文值原样保留')

// ---- ② 去重追加 / 删除 ----
ok(JSON.stringify(addValues(['a'], ['b', 'a', 'c'])) === JSON.stringify(['a', 'b', 'c']), '②追加：去重且保序（原值在前）')
ok(JSON.stringify(addValues(['a'], [''])) === JSON.stringify(['a']), '②追加：忽略空值')
ok(JSON.stringify(addValues([], ['x', 'x'])) === JSON.stringify(['x']), '②追加：空基＋重复 ⇒ 恰 1')
ok(JSON.stringify(removeValue(['a', 'b', 'a'], 'a')) === JSON.stringify(['b']), '②删除：移除全部同名')
ok(JSON.stringify(removeValue(['a'], 'zzz')) === JSON.stringify(['a']), '②删除：无匹配 ⇒ 原样')

// ---- ③ chip 文案：中文 label 优先、key 次级；未登记原样；冲突如实 ----
ok(chipParts('amount_cents', byKey).label === '金额', '③chip：英文键 ⇒ 中文 label「金额」')
ok(chipParts('amount_cents', byKey).conflict.length === 1 && chipParts('amount_cents', byKey).conflict[0] === 'SA：申请金额', '③chip：同名不同标签 ⇒ 冲突清单（不静默挑一个）')
ok(chipParts('经办状态', byKey).label === '经办状态' && chipParts('经办状态', byKey).known, '③chip：可写中文列 ⇒ 自标签且 known')
ok(chipParts('formula_flags', byKey).label === 'formula_flags', '③chip：无中文标签 ⇒ 回落原键')
ok(chipParts('some_future_key', byKey).known === false && chipParts('some_future_key', byKey).label === 'some_future_key', '③chip：未登记值 ⇒ 原样展示（可手动输入）')

// ---- ④ 下拉选项：排除已选、保 catalog 原序 ----
const opts = catalogOptions(CATALOG, ['amount_cents'])
ok(opts.length === CATALOG.length - 1 && !opts.some((it) => it.key === 'amount_cents'), '④下拉：已选项不再出现')
ok(JSON.stringify(opts.map((it) => it.key)) === JSON.stringify(['biz_no', 'member_purchaser', 'formula_flags', 'grp_state', '经办状态', '检验结论']), '④下拉：保目录原序（前端不重排）')
ok(optionText(CATALOG[0]) === '金额（amount_cents）', '④选项文案：中文（英文）')
ok(optionText(CATALOG[5]) === '经办状态', '④选项文案：自标签不重复显示')

// ---- ⑤ 实时合法值提示：目录内/外分列 ----
const st1 = manualStatus('amount_cents, 未知键', byKey)
ok(st1.values.length === 2 && st1.known.length === 1 && st1.known[0] === 'amount_cents' && st1.unknown.length === 1 && st1.unknown[0] === '未知键', '⑤提示：目录外值被点名（仍可保存）')
ok(manualStatus('amount_cents，biz_no', byKey).unknown.length === 0, '⑤提示：全目录内 ⇒ unknown 空')
ok(manualStatus('', byKey).values.length === 0, '⑤提示：空输入免判')

// ---- ⑥ ★ 保存契约未变：新旧构造逐字段等价 ----
// 改前：_allow 等为字符串，保存时 splitList(r._allow)；改后：数组直出。
// 不变量：同一份规则数据，两种构造产出的 JSON 必须逐字一致。
function splitList(text) {
  return String(text || '').split(/[,，\n]/).map((s) => s.trim()).filter(Boolean)
}
function joinList(arr) {
  return Array.isArray(arr) ? arr.join(', ') : ''
}
function oldPayload(rules) {
  return rules.map((r) => ({
    resource: r.resource,
    role: r.role,
    row_scope: r.row_scope,
    column_allow: splitList(r._allow),
    column_deny: splitList(r._deny),
    writable_fields: splitList(r._writable),
  }))
}
const ruleRows = [
  {
    resource: 'ledger:*',
    role: '综合运营主管',
    row_scope: 'ALL',
    column_allow: ['biz_no', 'member_purchaser'],
    column_deny: ['amount_cents', 'formula_flags'],
    _writable: ['经办状态', '检验结论'],
  },
  { resource: 'dashboard:1', role: '主管领导', row_scope: 'DEPT', _allow: [], _deny: [], _writable: [] },
]
// 改后形态：三列即数组（loadRules 直接拷贝自响应）
const newRows = ruleRows.map((r) => ({
  resource: r.resource,
  role: r.role,
  row_scope: r.row_scope,
  _allow: (r.column_allow || []).slice(),
  _deny: (r.column_deny || []).slice(),
  _writable: (r._writable || []).slice(),
}))
// 改前形态：同一数据经 joinList 落成字符串（模拟 loadRules 旧行为）
const oldRows = newRows.map((r) => ({
  resource: r.resource,
  role: r.role,
  row_scope: r.row_scope,
  _allow: joinList(r._allow),
  _deny: joinList(r._deny),
  _writable: joinList(r._writable),
}))
ok(JSON.stringify(buildSavePayload(newRows)) === JSON.stringify(oldPayload(oldRows)), '⑥契约：新构造（数组直出）=== 旧构造（join+split 往返）逐字一致')
ok(
  JSON.stringify(buildSavePayload(newRows)) ===
    JSON.stringify([
      { resource: 'ledger:*', role: '综合运营主管', row_scope: 'ALL', column_allow: ['biz_no', 'member_purchaser'], column_deny: ['amount_cents', 'formula_flags'], writable_fields: ['经办状态', '检验结论'] },
      { resource: 'dashboard:1', role: '主管领导', row_scope: 'DEPT', column_allow: [], column_deny: [], writable_fields: [] },
    ]),
  '⑥契约：载荷六字段名与 PUT /api/admin/permission-rules 请求体一致',
)

// ---- ⑦ datalist id：模块级计数器 ⇒ 跨实例不重复（一页多行组件同 id 会互相指向）----
const ids = new Set([nextDatalistId(), nextDatalistId(), nextDatalistId()])
ok(ids.size === 3 && [...ids].every((s) => typeof s === 'string' && s.startsWith('clist-datalist-')), '⑦id：三次调用互不重复')

if (failed > 0) {
  console.log(`\n${failed} 个断言失败`)
  throw new Error(`${failed} 个断言失败`) // 未捕获异常 ⇒ node 退出码 1（零依赖、无 process 全局假设）
}
console.log('\n全部断言通过')