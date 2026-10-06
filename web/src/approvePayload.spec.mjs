// N-069 T2② · buildApprovePayload 的机检断言（node 直跑，零依赖）。
// 运行：node web/src/approvePayload.spec.mjs
import assert from 'node:assert/strict'
import { buildApprovePayload } from './approvePayload.js'

// ① 无 record_fields ⇒ 载荷逐字不变（回归边界）。
assert.deepEqual(
  buildApprovePayload({ taskId: 't1', opinion: '同意' }),
  { task_id: 't1', opinion: '同意' },
  '空 recordFields 时载荷必须与既往逐字一致',
)
assert.deepEqual(
  buildApprovePayload({ taskId: 't1', opinion: '', recordFields: [], recordValues: {}, attachmentIds: [] }),
  { task_id: 't1', opinion: '' },
  '空数组/空附件也不得多出键',
)

// ② 有 record_fields ⇒ 只带非空键 + 数值转换 + 附件。
const rf = [
  { name: 'payment_receipt_no', type: 'text', required: true },
  { name: 'petty_cash_received_cents', type: 'money_cents', required: false },
  { name: 'petty_cash_receiver', type: 'person', required: false },
]
const body = buildApprovePayload({
  taskId: 't2',
  opinion: '',
  recordFields: rf,
  recordValues: { payment_receipt_no: 'PJ-1', petty_cash_received_cents: '50000', petty_cash_receiver: '  ' },
  attachmentIds: ['file-abc'],
})
assert.deepEqual(body, {
  task_id: 't2',
  opinion: '',
  fields: { payment_receipt_no: 'PJ-1', petty_cash_received_cents: 50000 },
  attachment_ids: ['file-abc'],
}, '非空键/数值转换/附件形态必须逐字')

// ③ 全空值 ⇒ 不带 fields 键（空值不送，权威拦截在后端）。
const body2 = buildApprovePayload({
  taskId: 't3',
  opinion: 'x',
  recordFields: rf,
  recordValues: { payment_receipt_no: '', petty_cash_receiver: '   ' },
})
assert.deepEqual(body2, { task_id: 't3', opinion: 'x' }, '全空值不得带 fields')

console.log('approvePayload.spec.mjs: 3 组断言全部通过')
