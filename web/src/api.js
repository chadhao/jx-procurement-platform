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
  put: (path, body) => request('PUT', path, { body }),
}

/** 判断错误是否为「未登录 / 未映射角色」，用于跳转登录页。 */
export function isAuthError(err) {
  return err instanceof ApiError && (err.code === CODE_UNAUTHORIZED || err.code === CODE_ROLE_MAPPED)
}

// ---- 领域接口 ----

/** 当前登录身份与可见范围摘要。 */
export const fetchMe = () => api.get('/api/me')

/** 飞书免登授权页 URL（公开端点；返回 { authorize_url }，含官方参数与防 CSRF state）。
 *  redirect：可选站内相对路径，后端绑定到 state Cookie，登录成功后 302 回跳该页。 */
export const fetchAuthorizeUrl = (redirect) =>
  api.get('/api/auth/authorize-url', redirect ? { redirect } : undefined)

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

// ---- 系统管理（M5，仅「系统管理员」；服务端二次校验）----

/** 权限矩阵（角色 × 资源）+ 枚举。 */
export const fetchPermissionRules = () => api.get('/api/admin/permission-rules')

/** 整表覆盖保存权限矩阵（保存后服务端清缓存 → 下一请求生效）。 */
export const savePermissionRules = (rules) => api.put('/api/admin/permission-rules', { rules })

/** 人员角色列表（含已停用）。 */
export const fetchAdminUsers = () => api.get('/api/admin/users')

/** 新增人员角色。 */
export const createAdminUser = (payload) => api.post('/api/admin/users', payload)

/** 修改人员角色（部分字段）。 */
export const patchAdminUser = (openId, payload) =>
  api.patch(`/api/admin/users/${encodeURIComponent(openId)}`, payload)

// ---- 运营性常量（T2 · R-24；只停用不删 —— DELETE 恒 40900，故不封装删除）----

/** 列出某常量表（table ∈ spec/constants.json；响应含 {table:{key,label,...}, items[]}）。 */
export const fetchConstants = (table, status) =>
  api.get('/api/admin/constants', { table, ...(status ? { status } : {}) })

/** 新增常量行（role_display_name 禁止新增 —— 后端 40000）。 */
export const createConstant = (payload) => api.post('/api/admin/constants', payload)

/** 改名 / 排序 / 停用启用（PUT /{id}）。 */
export const patchConstant = (id, payload) => api.put(`/api/admin/constants/${id}`, payload)

// ---- 角色代理人（N-028 · 授权配置；只停用不删 ⇒ 不封装删除）----

/** 列出代理人（响应含 items[] / eligible_roles[] / feature_enabled）。 */
export const fetchRoleAgents = (params) => api.get('/api/admin/role-agents', params)

/** 登记代理人（agent_open_id 值域＝通讯录镜像，前端点选、后端硬校验）。 */
export const createRoleAgent = (payload) => api.post('/api/admin/role-agents', payload)

/** 改人 / 备注 / 停用启用。 */
export const patchRoleAgent = (id, payload) => api.put(`/api/admin/role-agents/${id}`, payload)

/** 注销。 */
export const logout = () => api.post('/auth/logout')

// ---- 组织查询（转交/加签目标选择器数据源；★ 数据源＝t_user_role 已配置角色者，
//      非飞书通讯录全量，见 docs/05-API.md §3.15）----

/** 可选人员清单（仅启用中的角色映射；支持 { department, q } 筛选）。 */
export const fetchOrgUsers = (params) => api.get('/api/org/users', params)

/** 部门清单（t_user_role.department 去重、稳定排序）。 */
export const fetchOrgDepartments = () => api.get('/api/org/departments')

// ---- 看板（M5，FR-M5-01~08）----
// 说明：以下为新增导出，未改动上方任何既有导出（request / api / isAuthError / fetch* / save* 等）。
// 看板只读；数据由服务端按角色施加行级过滤 + 列级投影后返回，前端不传任何权限参数。

/** 看板指标数据。id：13 预算执行 / 14 采购执行 / 15 费用结构 / 16 异常预警。 */
export const fetchDashboard = (id, period) => api.get(`/api/dashboard/${id}`, { period })

/** 看板导出下载地址（format：csv / xlsx）。服务端复用同一行·列投影器，并写审计留痕。 */
export function dashboardExportUrl(id, period, format = 'csv') {
  const q = new URLSearchParams()
  if (period) q.set('period', period)
  q.set('format', format)
  return `/api/dashboard/${id}/export?${q.toString()}`
}

// ---- 集团报销跟踪表（M1，FR-M1-02）----
// 报销不进入本办法审批流程（人工走集团）；本资源承接「关联事前申请单号 → 移交 → 集团付款」的登记与查询。
// 权限：写＝综合运营主管；读＝综合运营主管 / 主管领导 / 项目总经理 / 系统管理员（服务端二次校验）。

/** 集团报销跟踪列表（分页；可按 src_biz_no / department / review_state 过滤）。 */
export const fetchReimbursements = (params) => api.get('/api/reimbursement', params)

/** 集团报销跟踪登记（综合运营主管）。 */
export const createReimbursement = (payload) => api.post('/api/reimbursement', payload)

/** 集团报销跟踪部分更新（集团侧付款字段仅人工登记，不回填）。 */
export const patchReimbursement = (id, payload) =>
  api.patch(`/api/reimbursement/${encodeURIComponent(id)}`, payload)

// ---- 变更链回溯（M4，FR-M4-07）----

/** 按合同号回溯历次变更（次数 / 累计金额 / 所取档位；行·列权限服务端裁剪）。 */
export const fetchContractChanges = (bizNo) =>
  api.get(`/api/contract/${encodeURIComponent(bizNo)}/changes`)

// ---- 审批流转（M9 · 转向 ③ 新增，契约见 docs/05-API.md §3.13）----
// ★ 说明：审批的「流转」已迁至我方系统（飞书只保留展示 / 待办 / 通知 + 两键）。
//   路径形如 `POST /api/approval/{biz_no}/<action>`，`{biz_no}` ＝ 业务单号（非 instance_id）。

/** 我的待办（数据源 t_flow_task：RELEASED ∧ PENDING ∧ assignee=me）。 */
export const fetchMyTasks = (params) => api.get('/api/approval/tasks', params)

/** 审批详情 + 时间线（时间线 ＝ t_flow_op_log，含四操作 + 回调）。 */
export const fetchApproval = (bizNo) => api.get(`/api/approval/${encodeURIComponent(bizNo)}`)

/** 三方审批定义清单（t_approval_def；管理页可选，系统管理员）。 */
export const fetchApprovalDefs = () => api.get('/api/approval/defs')

/** 发起页表单元数据（M3；含 spec_version / approval_codes / bands / enums）。 */
export const fetchApprovalMeta = () => api.get('/api/approval/meta')

/**
 * 分档/审批链预览（M7/D5）：与提交**共算同一入口**，前端不本地算分档。
 * 缺人时响应含 unresolved_roles（提交将被阻断，预览先行暴露）。
 */
export const previewApproval = (payload) => api.post('/api/approval/preview', payload)

/**
 * 我方提交：服务端算链 + 表单校验 + 回源标记。
 * `idempotencyKey` 可选：同键同载荷重放返回首次 biz_no（含 idempotent_replay 标记），
 * 同键异载荷 → 40900。
 */
export async function submitApproval(payload, idempotencyKey) {
  const headers = { 'Content-Type': 'application/json', Accept: 'application/json' }
  if (idempotencyKey) headers['Idempotency-Key'] = idempotencyKey
  const resp = await fetch('/api/approval/submit', {
    method: 'POST',
    credentials: 'same-origin',
    headers,
    body: JSON.stringify(payload),
  })
  return unwrapEnvelope(resp)
}

/**
 * 附件上传（M6）：multipart → 暂存，返回 {file_id, file_name, size_bytes}；
 * 随提交的 `attachment_ids` 在提交事务内绑定。
 */
export async function uploadApprovalAttachment(file) {
  const fd = new FormData()
  fd.append('file', file)
  const resp = await fetch('/api/approval/attachments', {
    method: 'POST',
    credentials: 'same-origin',
    body: fd, // ★ 不手设 Content-Type：浏览器自动带 boundary
  })
  return unwrapEnvelope(resp)
}

/** 解析标准 Envelope（{code,data,message,trace_id}），code!==0 抛 ApiError。 */
async function unwrapEnvelope(resp) {
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

/** 我方页面「同意」。与飞书回调共用同一状态机出口。 */
export const approveTask = (bizNo, body) =>
  api.post(`/api/approval/${encodeURIComponent(bizNo)}/approve`, body)

/** 我方页面「拒绝」。 */
export const rejectTask = (bizNo, body) =>
  api.post(`/api/approval/${encodeURIComponent(bizNo)}/reject`, body)

/** 转交：原任务 TRANSFERRED，新增同 node_id 任务。 */
export const transferTask = (bizNo, body) =>
  api.post(`/api/approval/${encodeURIComponent(bizNo)}/transfer`, body)

/**
 * 加签（＝顺序会签）：新增同 node_id 任务，按 `task_order` 插入。
 * ★ `timing ∈ {AFTER, BEFORE}` —— 由「操作人当场选」，缺省 `AFTER`（后置）。
 */
export const addsignTask = (bizNo, body) =>
  api.post(`/api/approval/${encodeURIComponent(bizNo)}/addsign`, body)

/** 回退：实例保持 PENDING，上一节点任务置回 PENDING。 */
export const rollbackTask = (bizNo, body) =>
  api.post(`/api/approval/${encodeURIComponent(bizNo)}/rollback`, body)

/** 撤回（仅发起人本人）：实例 → CANCELED；关闭流程、非删除。 */
export const cancelInstance = (bizNo, body) =>
  api.post(`/api/approval/${encodeURIComponent(bizNo)}/cancel`, body)
