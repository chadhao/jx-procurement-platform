// dateRange.spec.mjs —— N-054 ① 纯函数直跑（零依赖：node web/src/dateRange.spec.mjs）。
import { parseIsoInterval, buildIsoInterval } from './dateRange.js'

let failed = 0
function ok(cond, name) {
  if (cond) {
    console.log(`PASS ${name}`)
  } else {
    failed++
    console.log(`FAIL ${name}`)
  }
}

// parse：合法
const p1 = parseIsoInterval('2026-10-05/2026-10-20')
ok(p1.error === '' && p1.start === '2026-10-05' && p1.end === '2026-10-20', 'parse 合法区间')
// parse：空 ⇒ 无错（未填由结构化校验拦）
ok(parseIsoInterval('').error === '', 'parse 空串无错')
// parse：非法形态 ⇒ 可见 error、不返回半截数据
ok(parseIsoInterval('2026-10-01').error !== '', 'parse 单日期（缺斜杠）有 error')
ok(parseIsoInterval('2026-10-05/2026-10-20/2026-11-01').error !== '', 'parse 双斜杠有 error')
ok(parseIsoInterval('2026-10-20/2026-10-05').error !== '', 'parse 起止倒置有 error')
ok(parseIsoInterval('2026-13-40/2026-10-05').error !== '', 'parse 非法日期有 error')

// build：组装
ok(buildIsoInterval('2026-10-05', '2026-10-20').value === '2026-10-05/2026-10-20', 'build 合法组装 iso_interval')
const b2 = buildIsoInterval('', '')
ok(b2.value === '' && b2.error === '', 'build 全空 ⇒ 空值无错（提交不带该字段）')
ok(buildIsoInterval('2026-10-05', '').error !== '', 'build 只填一半有 error')
ok(buildIsoInterval('2026-10-20', '2026-10-05').error !== '', 'build 起>止有 error（不得提交）')
// 对称：build 后 parse 可回
const round = parseIsoInterval(buildIsoInterval('2026-09-28', '2026-10-03').value)
ok(round.error === '' && round.start === '2026-09-28', 'round-trip build→parse')

if (failed > 0) {
  console.log(`\n${failed} 个断言失败`)
  throw new Error(`${failed} 个断言失败`) // 未捕获异常 ⇒ node 退出码 1（零依赖、无 process 全局假设）
}
console.log('\n全部断言通过')
