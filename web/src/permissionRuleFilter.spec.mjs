// permissionRuleFilter.spec.mjs —— 权限矩阵「规则明细」分级筛选 · 纯函数直跑（零依赖）。
// 运行：node web/src/permissionRuleFilter.spec.mjs
//
// ★ 夹具（fixture）与生产同形：`{resource, role, row_scope, _allow, _deny, _writable}`
//   （见 web/src/views/Admin.vue#loadRules）。角色/资源名取自 internal/permission#Roles/#Resources。
// ★ 覆盖：① 资源去重 ② 角色分级联动（含「已选角色失效 ⇒ 自动重置」边界）
//   ③ 关键词（大小写不敏感 · 匹配 resource/role/可写字段）④ 组合筛选「资源×角色」恰 1 行
//   ⑤「全部」＝全集 ⑥ 归一化不误重置有效角色 · 空输入免判空。
//
// ★ 断言分组与命名：**单点变异**（去掉 roleOptionsFor 里那行资源收窄）应使
//   「②分级联动」两条恰红、其余保持绿 —— 故每条断言独立 ok()，不因一条失败而吞掉后续。
import { ALL, resourceOptions, roleOptionsFor, normalizeFilter, filterRules } from './permissionRuleFilter.js'

let failed = 0
function ok(cond, name) {
  if (cond) {
    console.log(`PASS ${name}`)
  } else {
    failed++
    console.log(`FAIL ${name}`)
  }
}

const ROLES = [
  '申请人', '主管领导', '项目总经理', '副总', '综合运营主管',
  '采购经办人', '验收人', '集团财务', '集团（审批）',
]
const RES = ['ledger:*', 'dashboard:1', 'dashboard:2', 'dashboard:3', 'dashboard:4', 'api:instances', 'api:audit']

function makeRow(resource, role, writable) {
  return { resource, role, row_scope: 'DENY', _allow: '', _deny: '', _writable: writable || '' }
}

// 全量夹具：角色 × 资源 全交叉（与 internal/seed#DefaultRules 同形）。
const full = []
for (const res of RES) {
  for (const role of ROLES) {
    full.push(makeRow(res, role, role === '综合运营主管' ? '付款凭据号, 抽查状态' : ''))
  }
}
ok(full.length === ROLES.length * RES.length, `夹具：角色 × 资源 全交叉 = ${ROLES.length} × ${RES.length} = ${full.length}`)

// ---- ① 资源选项：从 rules 实际出现者**去重**、保序、不硬编码 ----
const ropts = resourceOptions(full)
ok(ropts.length === RES.length, `①资源选项去重 = ${RES.length}`)
ok(JSON.stringify(ropts) === JSON.stringify([...new Set(RES)]), '①资源选项＝去重集且保序')

// ---- ① 组合筛选：资源 A + 角色 B ⇒ 恰 1 行，且就是 A×B ----
const ab = filterRules(full, { resource: 'api:audit', role: '集团财务' })
ok(ab.length === 1 && ab[0].resource === 'api:audit' && ab[0].role === '集团财务', '①资源×角色 ⇒ 恰 1 行（api:audit × 集团财务）')

// ---- ①「全部」＝ 全集（计数 M 来自数据，不硬编码）----
ok(
  filterRules(full, { resource: ALL, role: ALL, keyword: '' }).length === full.length,
  `①「全部」= ${full.length} 行（全集）`,
)

// ---- ② 分级联动：夹具中 dashboard:4 缺「综合运营主管」「集团（审批）」两角色 ----
const gapped = full.filter(
  (r) => !(r.resource === 'dashboard:4' && (r.role === '综合运营主管' || r.role === '集团（审批）')),
)
ok(roleOptionsFor(gapped, ALL).length === ROLES.length, '②资源=「全部」⇒ 角色下拉＝全集')
const narrowed = roleOptionsFor(gapped, 'dashboard:4')
ok(narrowed.length === ROLES.length - 2, `②选资源 dashboard:4 ⇒ 角色收窄到 ${narrowed.length}（缺 2）`)
ok(!narrowed.includes('综合运营主管') && !narrowed.includes('集团（审批）'), '②收窄后不含该资源下不存在的角色')

// ---- ② 边界（最易漏）：已选角色在新资源下不存在 ⇒ 自动重置为「全部角色」----
const nf = normalizeFilter(gapped, { resource: 'dashboard:4', role: '综合运营主管', keyword: '' })
ok(nf.role === ALL, '②已选角色失效 ⇒ 自动重置为「全部角色」（边界）')
ok(nf.resource === 'dashboard:4', '②重置只动角色、不动资源')

// ---- ② 有效角色不被误重置；资源改回「全部」角色恢复全集 ----
ok(normalizeFilter(gapped, { resource: 'dashboard:4', role: '申请人', keyword: '' }).role === '申请人', '②有效角色保持（不误重置）')
ok(roleOptionsFor(gapped, ALL).length === ROLES.length, '②资源改回「全部」⇒ 角色恢复全集')

// ---- ③ 关键词：大小写不敏感 · 匹配 resource / role / 可写字段 ----
ok(filterRules(full, { keyword: 'api:' }).length === 2 * ROLES.length, '③关键词匹配资源（api: ⇒ 2 资源 × 全角色）')
ok(
  filterRules(full, { keyword: 'API:' }).length === filterRules(full, { keyword: 'api:' }).length,
  '③关键词大小写不敏感（API: ≡ api:）',
)
ok(filterRules(full, { keyword: 'ledger' }).length === ROLES.length, '③关键词匹配资源名（ledger ⇒ 1 资源 × 全角色）')
ok(filterRules(full, { keyword: '验收人' }).length === RES.length, '③关键词匹配角色（验收人 ⇒ 全资源）')
ok(filterRules(full, { keyword: '付款凭据号' }).length === RES.length, '③关键词匹配可写字段（付款凭据号 ⇒ 该角色 × 全资源）')
ok(filterRules(full, { keyword: '不存在XYZ' }).length === 0, '③无匹配 ⇒ 0 行（驱动空态文案）')

// ---- ③' _writable 数组形态（★ 可编辑列表改后行内模型即数组，Admin.vue#loadRules）----
// 同一份数据两种形态：字符串（改前逗号串）vs 数组（改后可编辑列表）⇒ 筛选结果必须一致。
const strRows = full.filter((r) => r.role === '综合运营主管')
const arrRows = strRows.map((r) => ({
  ...r,
  _writable: r._writable ? r._writable.split(',').map((s) => s.trim()).filter(Boolean) : [],
}))
ok(arrRows.every((r) => Array.isArray(r._writable)) && arrRows[0]._writable.length === 2, "③'夹具：数组形态 _writable（2 值）")
ok(filterRules(arrRows, { keyword: '付款凭据号' }).length === RES.length, "③'数组 _writable：关键词命中（该角色 × 全资源）")
ok(
  filterRules(arrRows, { keyword: '付款凭据号' }).length === filterRules(strRows, { keyword: '付款凭据号' }).length &&
    filterRules(arrRows, { keyword: '抽查' }).length === filterRules(strRows, { keyword: '抽查' }).length,
  "③'数组与字符串两种 _writable 形态筛选结果一致（同数据）",
)
ok(filterRules(arrRows, { keyword: '不存在XYZ' }).length === 0, "③'数组 _writable：无匹配 ⇒ 0 行")

// ---- ③ 计数随筛选变化（N = 过滤后行数）----
ok(filterRules(full, { resource: 'dashboard:1' }).length === ROLES.length, '③计数：dashboard:1 ⇒ N＝全角色数')
ok(filterRules(full, { resource: ALL, role: '副总' }).length === RES.length, '③计数：角色=副总 ⇒ N＝资源数')

// ---- ⑥ 归一化保留关键词；空输入免判空 ----
ok(normalizeFilter(full, { keyword: 'abc' }).keyword === 'abc', '⑥归一化保留关键词')
ok(normalizeFilter(full, {}).role === ALL, '⑥空筛选 ⇒ 角色＝「全部」')
ok(resourceOptions([]).length === 0, '⑥空 rules ⇒ 资源选项 0')
ok(roleOptionsFor([], ALL).length === 0, '⑥空 rules ⇒ 角色选项 0')
ok(filterRules([], { resource: 'x', role: 'y' }).length === 0, '⑥空 rules ⇒ 过滤 0 行')

if (failed > 0) {
  console.log(`\n${failed} 个断言失败`)
  throw new Error(`${failed} 个断言失败`) // 未捕获异常 ⇒ node 退出码 1
}
console.log('\n全部断言通过')
