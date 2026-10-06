// 审批/动作提交载荷构造（N-069 T2② · 纯函数 · 零依赖 —— 可被 Node 直跑断言）。
//
// ★ 回归边界（spec/chain.json#conventions.node_record_fields）：
//   · 该任务**无 `record_fields`**（或全空）⇒ 载荷＝**逐字** `{task_id, opinion}`
//     —— 既有调用方零影响；
//   · 有 `record_fields` ⇒ 只带**渲染过的键**（空值不送 ⇒ 后端判据仍权威拦截），
//     数值类（number / money_cents）转 Number（金额口径＝分，见 chain.json money_note）；
//   · 附件走**既有暂存通道**拿 file_id ⇒ `attachment_ids`（可选，空不带键）。
export function buildApprovePayload({ taskId, opinion, recordValues, recordFields, attachmentIds }) {
  const body = { task_id: String(taskId ?? ''), opinion: opinion ?? '' }
  const fields = {}
  let n = 0
  for (const f of recordFields || []) {
    const raw = recordValues ? recordValues[f.name] : undefined
    if (raw === undefined || raw === null || String(raw).trim() === '') continue
    if (f.type === 'number' || f.type === 'money_cents') {
      const num = Number(raw)
      fields[f.name] = Number.isFinite(num) ? num : raw
    } else {
      fields[f.name] = raw
    }
    n++
  }
  if (n > 0) body.fields = fields
  if (attachmentIds && attachmentIds.length) {
    body.attachment_ids = attachmentIds.slice()
  }
  return body
}
