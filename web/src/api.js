// api.js —— 统一请求封装。
// 后端所有响应包裹为 { code, data, message, trace_id }（见 docs/05-API.md §2）。
// code === 0 视为成功并返回 data；否则抛出带 code/message 的错误对象。

const CODE_UNAUTHORIZED = 40100
const CODE_ROLE_MAPPED = 40101

/** 业务错误对象。 */
export class ApiError extends Error {
  constructor(code, message, traceId) {
    super(message || '请求失败')
    this.name = 'ApiError'
    this.code = code
    this.traceId = traceId
  }
}

async function request(method, path, { query, body } = {}) {
  const url = new URL(path, window.location.origin)
  if (query) {
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null && v !== '') {
        url.searchParams.set(k, v)
      }
    }
  }
  const init = {
    method,
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  }
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  const resp = await fetch(url.pathname + url.search, init)
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

export const api = {
  get: (path, query) => request('GET', path, { query }),
  patch: (path, body) => request('PATCH', path, { body }),
  post: (path, body) => request('POST', path, { body }),
}

/** 判断错误是否为「未登录 / 未映射角色」，用于跳转登录页。 */
export function isAuthError(err) {
  return err instanceof ApiError && (err.code === CODE_UNAUTHORIZED || err.code === CODE_ROLE_MAPPED)
}

// ---- 领域接口 ----

/** 当前登录身份与可见范围摘要。 */
export const fetchMe = () => api.get('/api/me')

/** 实例列表（分页）。 */
export const fetchInstances = (params) => api.get('/api/instances', params)

/** 实例详情。 */
export const fetchInstance = (code) => api.get(`/api/instances/${encodeURIComponent(code)}`)

/** 实例字段明细。 */
export const fetchInstanceFields = (code) => api.get(`/api/instances/${encodeURIComponent(code)}/fields`)

/** 实例状态时间线（追加式，保留驳回→重提全链）。 */
export const fetchInstanceTimeline = (code) => api.get(`/api/instances/${encodeURIComponent(code)}/timeline`)

/** 台账列表。 */
export const fetchLedger = (table, params) => api.get(`/api/ledger/${encodeURIComponent(table)}`, params)

/** 台账单条。 */
export const fetchLedgerRow = (table, id) => api.get(`/api/ledger/${encodeURIComponent(table)}/${id}`)

/** 台账运营字段写回。 */
export const patchLedgerRow = (table, id, fields) => api.patch(`/api/ledger/${encodeURIComponent(table)}/${id}`, { fields })

/** 审计日志。 */
export const fetchAuditLogs = (params) => api.get('/api/audit/logs', params)

/** 注销。 */
export const logout = () => api.post('/auth/logout')
