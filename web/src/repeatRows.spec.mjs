// repeatRows.spec.mjs —— N-050 纯函数直跑测试（零依赖：node web/src/repeatRows.spec.mjs）。
// 鉴别力覆盖：T6 变异2（列多静默截断）与变异3（locate 只读第 0 项）在此转红。
import { rowEditableFieldsOf, parseBulkRows, locateFormErrors } from './repeatRows.js'

let failed = 0
function ok(cond, name) {
  if (cond) {
    console.log(`PASS ${name}`)
  } else {
    failed++
    console.log(`FAIL ${name}`)
  }
}

const fields = [
  { name: 'a', source: 'user' },
  { name: 'b', source: 'user' },
  { name: 'sys', source: 'system' },
]

// rowEditableFieldsOf：只收 source=user
ok(rowEditableFieldsOf({ fields }).map((f) => f.name).join(',') === 'a,b', 'rowEditableFieldsOf 只收 user')
ok(rowEditableFieldsOf(null).length === 0, 'rowEditableFieldsOf 空入参不抛')

// parseBulkRows：Tab 分列 / 无 Tab 逗号 / 丢空行 / 列少留空
const t1 = parseBulkRows(fields, 'x\ty\n\nz\tw')
ok(t1.error === '' && t1.rows.length === 2 && t1.rows[0].a === 'x' && t1.rows[1].b === 'w', 'parseBulkRows Tab+丢空行')
ok(t1.rows[0].sys === '' && t1.rows[0].a === 'x', 'parseBulkRows 列少⇒其余留空、值原样字符串')
const t2 = parseBulkRows(fields, 'x,y')
ok(t2.error === '' && t2.rows.length === 1 && t2.rows[0].b === 'y', 'parseBulkRows 无 Tab 退化逗号')
// ★ 变异2 鉴别力：列多于字段数必须可见报错（静默截断 ⇒ 此断言红）。
// ★ parseBulkRows 的 fields 入参＝可编辑字段（调用方 rowEditableFields 过滤后传入）。
const editable = rowEditableFieldsOf({ fields })
const t3 = parseBulkRows(editable, 'a,b,c')
ok(t3.error !== '' && t3.rows.length === 0, 'parseBulkRows 列多于字段数 ⇒ error 可见（不静默丢弃）')

// locateFormErrors：全数组遍历
const l1 = locateFormErrors([
  { section_id: 'detail', row_index: 2, field_name: 'm' },
  { section_id: 'detail', row_index: 0, field_name: '' },
])
ok(l1.keys.length === 2, 'locateFormErrors keys 必须遍历全数组（≥2 条）')
ok(l1.first && l1.first.row_index === 2 && l1.first.field_name === 'm', 'locateFormErrors first=首个可定位项')
// ★ 变异3 鉴别力：只读第 0 项 ⇒ 第 2 条键丢失
const l2 = locateFormErrors([
  { section_id: 's', row_index: 1, field_name: 'x' },
  { section_id: 's', row_index: 3, field_name: 'y' },
])
ok(l2.keys.includes('s|3|y'), 'locateFormErrors 双元素用例：第 2 条键必须在（只读第 0 项即红）')
// 非数组入参不抛
const l3 = locateFormErrors('nope')
ok(l3.first === null && l3.keys.length === 0, 'locateFormErrors 非数组 ⇒ {first:null,keys:[]} 不抛')

if (failed > 0) {
  console.log(`\n${failed} 个断言失败`)
  throw new Error(`${failed} 个断言失败`) // 未捕获异常 ⇒ node 退出码 1（零依赖、无 process 全局假设）
}
console.log('\n全部断言通过')
