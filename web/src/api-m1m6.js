// api-m1m6.js —— M1 备付金 / M6 报送 的接口 helper。
//
// 复用 api.js 的 request 封装与 ApiError（不修改 api.js）：绝大多数调用直接走 `api.*`；
// 仅「报送登记」需携带 Idempotency-Key 请求头，故在 api.js 的 request 语义之上补一个头，
// 保证错误语义（{code,data,message,trace_id} 且 code!==0 抛 ApiError）与 api.js 完全一致。

import { api, ApiError } from './api.js'

// ---- M1 备付金 ----

/** 备付金余额（只读；period 缺省为当月）。 */
export const fetchPettyCashBalance = (period) => api.get('/api/petty-cash/balance', { period })

/** 备付金签领登记（综合运营主管）。 */
export const createPettyCashReceipt = (payload) => api.post('/api/petty-cash/receipt', payload)

/** 备付金月核销（唯一账期一条；重复 → 40900）。 */
export const monthlyClosePettyCash = (payload) => api.post('/api/petty-cash/monthly-close', payload)

// ---- M6 报送 ----

/** 报送记录列表 / 月度「已提交未付款」对账清单。 */
export const fetchSubmissions = (params) => api.get('/api/submission', params)

/** 移交凭证（签收记录）登记 / 补填；补填后「未提交」→「已提交」。 */
export const registerSubmissionReceipt = (id, receiptRef) =>
  api.post(`/api/submission/${id}/receipt`, { receipt_ref: receiptRef })

/** 集团侧字段人工登记（受理编号 / 流程状态 / 付款完成日期 / 状态）。 */
export const registerSubmissionGroup = (id, payload) => api.post(`/api/submission/${id}/group`, payload)

/** 集团驳回处置登记（取消 / 驳回重走 + 原因）。 */
export const registerSubmissionReject = (id, payload) => api.post(`/api/submission/${id}/reject`, payload)

/** 凭证包下载地址（zip/pdf，同源直链，携带会话 Cookie）。 */
export const submissionPackageUrl = (id, format = 'zip') =>
  `/api/submission/${encodeURIComponent(id)}/package?format=${encodeURIComponent(format)}`

/**
 * 提交集团登记（携带 Idempotency-Key，避免重复点击 / 网络重试产生重复记录）。
 *
 * ★ 幂等键必须由调用方**跨重试复用**（见 Submission.vue 的 currentIdemKey）：
 *   若每次调用都新生成键，重复点击会拿到两个不同的键 → 服务端视为两次独立登记，
 *   幂等机制形同虚设。故此处把键作为入参，只在调用方缺省时才兜底生成。
 *
 * 服务端语义（docs/05-API.md §2.2 / §8）：
 *   - 同键 + 同载荷 → 200，响应体为**首次登记结果**（`idempotent_replay=true`）；
 *   - 同键 + 异载荷 → 40900（幂等冲突，抛 ApiError）。
 *
 * @param {object} payload 报送登记请求体
 * @param {string} [idempotencyKey] 本次「登记意图」的稳定幂等键；同一意图重试必须复用
 * @returns {Promise<{id:number, biz_no:string, submit_state:string, overdue:boolean, idempotent_replay?:boolean}>}
 */
export async function createSubmission(payload, idempotencyKey) {
  const key = idempotencyKey || genIdempotencyKey()
  const resp = await fetch('/api/submission', {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      'Idempotency-Key': key,
    },
    body: JSON.stringify(payload),
  })
  let env
  try {
    env = await resp.json()
  } catch (e) {
    throw new ApiError(resp.status * 100, `响应解析失败（HTTP ${resp.status}）`)
  }
  if (!env || env.code !== 0) {
    throw new ApiError(env ? env.code : -1, env ? env.message : '未知错误', env ? env.trace_id : '')
  }
  return env.data
}

/** 生成一个幂等键（优先 UUID v4）。调用方须在「同一次登记意图」内复用同一个键。 */
export function genIdempotencyKey() {
  if (window.crypto && typeof window.crypto.randomUUID === 'function') {
    return window.crypto.randomUUID()
  }
  return 'sub-' + Date.now() + '-' + Math.random().toString(16).slice(2)
}
