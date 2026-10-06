<script setup>
// 审批操作台（M9 · 转向 ③ 新增，契约见 docs/05-API.md §3.13）。
//
// 职责：
//   · 我方页面「同意 / 拒绝」两键（与飞书回调共用同一状态机出口，FR-M9-18）；
//   · 四操作：转交 / 加签 / 回退 / 撤回（飞书侧无这些按钮，全在我方页面完成）。
// ★ 加签（＝顺序会签）的「前置 / 后置」由**操作人当场选**（`timing ∈ {AFTER, BEFORE}`，
//   缺省 `AFTER`）—— 用户裁定，见 internal/flow/ops.go AddSignTiming / 01a §4.3。
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import {
  fetchApproval,
  approveTask,
  rejectTask,
  transferTask,
  addsignTask,
  rollbackTask,
  cancelInstance,
  fetchOrgUsers,
  fetchOrgDepartments,
  uploadApprovalAttachment,
} from '../api'
import { buildApprovePayload } from '../approvePayload'
import { session } from '../store'
import { statusClass, statusLabel, fmtTime, opTypeLabel } from '../utils'

const route = useRoute()
const bizNo = String(route.params.bizNo || '')

const loading = ref(true)
const err = ref('')
const detail = ref(null)
const notice = ref('') // 操作成功提示
const busy = ref(false)

// 两键的意见
const opinion = ref('')

// ★ N-067②③ / N-069 T2：本环节「记录字段」录入区（数据驱动 —— 键/标签/类型/必填
//   全部来自详情接口任务行的 `record_fields`（spec 读出），**前端零硬编码字段名**）。
//   仅当选中任务带 record_fields 时渲染；附件走既有暂存上传通道拿 file_id。
const recordValues = reactive({})
const recordFiles = ref([]) // [{file_id, file_name}]（当前渲染字段的附件槽）
const recordUploading = ref(false)

const recordFields = computed(() => (selectedTask.value && selectedTask.value.record_fields) || [])

function resetRecordInput() {
  for (const k of Object.keys(recordValues)) delete recordValues[k]
  recordFiles.value = []
}

// 字段类型 → 控件类型（类型词表属 schema 级、非字段名 —— 不违反「不硬编码字段名」）。
function recordInputType(f) {
  if (f.type === 'number' || f.type === 'money_cents') return 'number'
  if (f.type === 'date') return 'date'
  return 'text'
}

// 附件类字段：经既有暂存通道上传（POST /api/approval/attachments）→ file_id。
async function uploadRecordFile(ev, f) {
  const file = ev.target.files && ev.target.files[0]
  if (!file) return
  recordUploading.value = true
  err.value = ''
  try {
    const res = await uploadApprovalAttachment(file)
    // 单字段单槽位：新上传覆盖旧选择（staging 未绑定者由服务端惰性清理）。
    recordFiles.value = [{ file_id: res.file_id, file_name: res.file_name || file.name, field: f.name }]
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    recordUploading.value = false
    ev.target.value = ''
  }
}

// 四操作表单（op.type 为空＝不展开）
const op = reactive({
  type: '',
  target_open_id: '',
  reason: '',
  timing: 'AFTER', // ★ 加签时机：缺省「后置」
  target_node_id: '',
})

// 当前选中的待办任务（默认：路由 query.task_id；否则第一个 PENDING）
const selectedTaskId = ref(String(route.query.task_id || ''))

// 详情主记录（兼容 { instance:{...} } 与直接对象两种形状）
const main = computed(() => (detail.value && (detail.value.instance || detail.value.main)) || detail.value || {})
const tasks = computed(() => (detail.value && detail.value.tasks) || [])
const ops = computed(() => (detail.value && detail.value.ops) || [])

const meOpenId = computed(() => (session.me && session.me.open_id) || '')

const selectedTask = computed(() => tasks.value.find((t) => String(t.task_id) === String(selectedTaskId.value)) || null)

// 切换任务 ⇒ 清空上一任务的记录字段草稿/附件选择（防串写）。
watch(selectedTaskId, () => {
  resetRecordInput()
})

// ---- 办理人展示（解绿框）----
// ★ 后端已补 `assignee_name` / `assignee_department`（数据源 t_user_role）。
//   展示形态＝「人名（部门）」；任一缺失的回落：有名无部门 → 只显示名；都无 → `-`。
//   ★★ 绝不回落显示裸 open_id（用户明确反馈不要看到 ou_xxx）。
function assigneeText(t) {
  const name = String(t.assignee_name || '').trim()
  const dept = String(t.assignee_department || '').trim()
  if (name && dept) return `${name}（${dept}）`
  if (name) return name
  return '-'
}

// 「本人可办」的任务：PENDING 且（无 assignee 信息 或 assignee = 我）
function isMine(t) {
  const a = t.assignee_open_id || t.assignee
  return !a || !meOpenId.value || a === meOpenId.value
}

const actionable = computed(() => {
  const t = selectedTask.value
  return !!t && String(t.status || '').toUpperCase() === 'PENDING' && isMine(t)
})

const applicantText = computed(() => {
  const a = main.value.applicant
  if (!a) return '-'
  if (typeof a === 'string') return a
  return a.name || a.open_id || '-'
})

const currentNode = computed(() => {
  const t = tasks.value.find((x) => String(x.status || '').toUpperCase() === 'PENDING')
  return (t && (t.node_name || t.node_id)) || '-'
})

async function load() {
  loading.value = true
  err.value = ''
  try {
    detail.value = await fetchApproval(bizNo)
    // 选中任务回退：query → 第一个 PENDING → 第一个任务
    const list = tasks.value
    if (!list.some((t) => String(t.task_id) === String(selectedTaskId.value))) {
      const pend = list.find((t) => String(t.status || '').toUpperCase() === 'PENDING')
      selectedTaskId.value = String((pend && pend.task_id) || (list[0] && list[0].task_id) || '')
    }
  } catch (e) {
    err.value = e.message || String(e)
    detail.value = null
  } finally {
    loading.value = false
  }
}

function resetOp() {
  op.type = ''
  op.target_open_id = ''
  op.reason = ''
  op.timing = 'AFTER'
  op.target_node_id = ''
  opQuery.value = ''
}

// ---- 办理人选择器（解红框：不再手填 open_id，改为「部门筛选 + 姓名搜索 + 点选」）----
// ★ 数据源：GET /api/org/users 与 GET /api/org/departments（t_user_role＝已配置角色者，
//   非飞书通讯录全量 —— 转交/加签目标必须是有权限的审批人）。
// ★ 提交给后端的字段名保持 `target`（契约不变），另附既有展示字段 `target_name`。
const orgUsers = ref([]) // 全量可选人员（一次拉取，前端本地过滤）
const orgDepts = ref([]) // 部门清单（去重、稳定排序）
const orgLoading = ref(false)
const orgLoaded = ref(false)
const opDept = ref('') // 部门筛选（''＝全部部门）
const opQuery = ref('') // 姓名关键字（前端本地过滤）

async function ensureOrgData() {
  if (orgLoaded.value || orgLoading.value) return
  orgLoading.value = true
  try {
    const [users, depts] = await Promise.all([fetchOrgUsers(), fetchOrgDepartments()])
    orgUsers.value = (users && users.items) || []
    orgDepts.value = (depts && depts.items) || []
    orgLoaded.value = true
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    orgLoading.value = false
  }
}

// 切到「转交 / 加签」时才拉人员/部门清单（懒加载：无操作需求的访问不多打接口）。
watch(
  () => op.type,
  (v) => {
    if (v === 'transfer' || v === 'addsign') ensureOrgData()
  },
)

const filteredUsers = computed(() => {
  const dept = opDept.value.trim()
  const q = opQuery.value.trim()
  return orgUsers.value.filter((u) => {
    if (dept && String(u.department || '') !== dept) return false
    if (q && !String(u.name || '').includes(q)) return false
    return true
  })
})

const selectedUser = computed(
  () => orgUsers.value.find((u) => u.open_id === op.target_open_id) || null,
)

function pickUser(u) {
  op.target_open_id = u.open_id
}

function clearTarget() {
  op.target_open_id = ''
  opQuery.value = ''
}

// 转交 / 加签需要目标人；回退 / 撤回不需要。
const needsTarget = computed(() => op.type === 'transfer' || op.type === 'addsign')

function requireTask() {
  if (!selectedTaskId.value) {
    notice.value = ''
    err.value = '请先在上方「任务」中选择要办理的任务'
    return false
  }
  return true
}

async function run(fn, okText) {
  err.value = ''
  notice.value = ''
  busy.value = true
  try {
    await fn()
    notice.value = okText
    resetOp()
    opinion.value = ''
    resetRecordInput()
    await load()
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}

function doApprove() {
  if (!requireTask()) return
  // ★ 前端必填提示（**仅体验**）：权威拦截在后端（checkReceiptPerPurchase 等判据）——
  //   前端可绕、判据不可绕，此处缺项即拦下请求并点名字段。
  for (const f of recordFields.value) {
    const v = recordValues[f.name]
    const hasVal = v !== undefined && v !== null && String(v).trim() !== ''
    if (f.type === 'attachment') {
      if (f.required && !recordFiles.value.length) {
        err.value = `请上传「${f.label || f.name}」（后端判据将拒绝空值）`
        return
      }
    } else if (f.required && !hasVal) {
      err.value = `请填写「${f.label || f.name}」（后端判据将拒绝空值）`
      return
    }
  }
  // ★ 载荷经纯函数构造（approvePayload.js · spec.mjs 机检）：无 record_fields ⇒ 逐字不变。
  const body = buildApprovePayload({
    taskId: selectedTaskId.value,
    opinion: opinion.value,
    recordValues,
    recordFields: recordFields.value,
    attachmentIds: recordFiles.value.map((x) => x.file_id),
  })
  const isRecordNode = recordFields.value.length > 0
  // ★ R-36⑦：动作型待办不沿用审批语态 —— 有记录字段时按钮/成功文案改「提交」。
  run(() => approveTask(bizNo, body), isRecordNode ? '已提交' : '已同意')
}

function doReject() {
  if (!requireTask()) return
  run(() => rejectTask(bizNo, { task_id: selectedTaskId.value, opinion: opinion.value }), '已拒绝')
}

function doOp() {
  if (op.type === 'cancel') {
    run(() => cancelInstance(bizNo, { reason: op.reason }), '已撤回')
    return
  }
  if (!requireTask()) return
  // ★ 目标人必选（防提交空 target）：未选中时「执行」按钮本已禁用，这里双保险。
  if (needsTarget.value && !op.target_open_id) {
    err.value = '请选择办理人（支持按部门 / 姓名筛选）'
    return
  }
  // ★ 字段名对齐已实现的后端 handler（internal/httpapi/handlers_approval.go
  //   `approvalActionBody`）：目标 open_id ＝ `target`、目标姓名（展示）＝ `target_name`、
  //   回退目标节点 ＝ `target_node`。
  const targetUser = selectedUser.value
  const base = {
    task_id: selectedTaskId.value,
    reason: op.reason,
    target_name: targetUser ? targetUser.name || '' : undefined,
  }
  if (op.type === 'transfer') {
    run(() => transferTask(bizNo, { ...base, target: op.target_open_id }), '已转交')
  } else if (op.type === 'addsign') {
    // ★ `timing` 由操作人当场选（默认 AFTER）：与 handler 的 `timing` 字段一致。
    run(
      () => addsignTask(bizNo, { ...base, target: op.target_open_id, timing: op.timing || 'AFTER' }),
      '已加签',
    )
  } else if (op.type === 'rollback') {
    run(() => rollbackTask(bizNo, { ...base, target_node: op.target_node_id || undefined }), '已回退')
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="panel">
      <div class="toolbar">
        <router-link to="/tasks">← 我的待办</router-link>
        <router-link to="/instances">审批实例</router-link>
      </div>

      <div v-if="err" class="error">{{ err }}</div>
      <div v-if="notice" class="tag ok" style="display: inline-block">{{ notice }}</div>
      <div v-if="loading" class="empty">加载中…</div>

      <template v-else-if="detail">
        <h2>审批操作台 · {{ bizNo }}</h2>
        <dl class="kv">
          <dt>状态</dt>
          <dd><span class="tag" :class="statusClass(main.status)">{{ statusLabel(main.status) }}</span></dd>
          <dt>单据类型</dt>
          <dd>{{ main.doc_type || '-' }}</dd>
          <dt>申请人</dt>
          <dd>{{ applicantText }}</dd>
          <dt>金额</dt>
          <dd>{{ main.amount_display || '-' }}</dd>
          <dt>当前节点</dt>
          <dd>{{ currentNode }}</dd>
        </dl>
      </template>
      <div v-else class="empty">
        未取到审批详情。若端点尚在装配（#54），请稍后重试；本页在端点就绪后无需改动。
      </div>
    </div>

    <div v-if="detail" class="panel">
      <h2>任务</h2>
      <div v-if="!tasks.length" class="empty">暂无任务</div>
      <div v-else class="table-scroll">
        <table>
          <thead>
            <tr>
              <th></th>
              <th>节点</th>
              <th>办理人</th>
              <th>状态</th>
              <th>释放态</th>
              <th>顺序</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in tasks" :key="t.task_id">
              <td>
                <input
                  type="radio"
                  name="task"
                  :value="String(t.task_id)"
                  v-model="selectedTaskId"
                  :disabled="String(t.status || '').toUpperCase() !== 'PENDING'"
                />
              </td>
              <td>{{ t.node_name || t.node_id || '-' }}</td>
              <td>{{ assigneeText(t) }}</td>
              <td><span class="tag" :class="statusClass(t.status)">{{ statusLabel(t.status) }}</span></td>
              <td>{{ t.release_state || '-' }}</td>
              <td>{{ t.task_order != null ? t.task_order : '-' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="muted">★ 任一非终态实例恰 1 个可办理任务（顺序会签不变量）。请先在「节点」前选中要办理的任务。</p>
    </div>

    <!-- ★ 黄框（用户实测反馈）：无「本人可办理任务」时本面板整体不渲染（复用既有 actionable 判据） -->
    <div v-if="detail && actionable" class="panel">
      <h2>审批操作</h2>

      <div class="toolbar">
        <input v-model="opinion" placeholder="意见（可选）" @keyup.enter="doApprove" />
        <button class="primary" :disabled="busy || !actionable || recordUploading" @click="doApprove">
          {{ recordFields.length ? '提交' : '同意' }}
        </button>
        <button class="danger" :disabled="busy || !actionable" @click="doReject">拒绝</button>
      </div>

      <!-- ★ N-067②③/N-069 T2：本环节记录字段（record_fields 驱动 · 仅动作型待办出现；
           无 record_fields 的节点本区块不渲染、载荷逐字不变（回归边界） -->
      <template v-if="recordFields.length">
        <h3 style="margin: 12px 0 4px">本环节记录字段</h3>
        <div v-for="f in recordFields" :key="f.name" class="inline-form">
          <label>
            {{ f.label || f.name }}<span v-if="f.required" style="color: var(--danger, #c00)">*</span>
          </label>
          <template v-if="f.type === 'attachment'">
            <input type="file" :disabled="recordUploading || busy" @change="(ev) => uploadRecordFile(ev, f)" />
            <span v-if="recordFiles.length" class="tag ok" style="display: inline-block">
              已选 {{ recordFiles.map((x) => x.file_name).join('、') }}
            </span>
            <span v-else class="muted">未上传</span>
          </template>
          <input
            v-else
            :type="recordInputType(f)"
            v-model="recordValues[f.name]"
            :placeholder="f.type === 'money_cents' ? '单位：分' : ''"
            :disabled="busy"
          />
        </div>
      </template>
      <p v-if="!actionable" class="muted">「同意 / 拒绝」须选中一个「审批中」的、且指派给你的任务。</p>

      <hr style="border: none; border-top: 1px solid var(--border); margin: 16px 0" />

      <div class="inline-form">
        <label>四操作</label>
        <select v-model="op.type">
          <option value="">（不执行）</option>
          <option value="transfer">转交</option>
          <option value="addsign">加签</option>
          <option value="rollback">回退</option>
          <option value="cancel">撤回（仅发起人）</option>
        </select>
      </div>

      <!-- ★ 红框改造（用户实测反馈）：不再手填 open_id —— 部门下拉 + 姓名搜索 + 点选；
           数据源＝GET /api/org/users、GET /api/org/departments（t_user_role，非通讯录全量） -->
      <template v-if="op.type && op.type !== 'cancel'">
        <div class="inline-form">
          <label>部门</label>
          <select v-model="opDept">
            <option value="">全部部门</option>
            <option v-for="dp in orgDepts" :key="dp" :value="dp">{{ dp }}</option>
          </select>
        </div>
        <div class="inline-form">
          <label>办理人</label>
          <input v-model="opQuery" placeholder="输入姓名关键字搜索" />
        </div>
        <div v-if="selectedUser" class="inline-form">
          <label>已选办理人</label>
          <span class="tag ok" style="display: inline-block">
            {{ selectedUser.name || '(未命名)' }}（{{ selectedUser.department || '-' }}）
          </span>
          <button type="button" @click="clearTarget">清除重选</button>
        </div>
        <div class="user-pick-list">
          <div v-if="orgLoading" class="empty">人员清单加载中…</div>
          <div v-else-if="!orgUsers.length" class="empty">暂无可选人员（系统内尚未配置角色）</div>
          <div v-else-if="!filteredUsers.length" class="empty">无匹配人员，请调整部门或关键字</div>
          <ul v-else class="user-pick">
            <li
              v-for="u in filteredUsers"
              :key="u.open_id"
              :class="{ picked: u.open_id === op.target_open_id }"
              @click="pickUser(u)"
            >
              {{ u.name || '(未命名)' }}（{{ u.department || '-' }}）· {{ u.role || '-' }}
            </li>
          </ul>
        </div>
      </template>

      <!-- ★ 加签时机：操作人当场选；缺省「后置（AFTER）」 -->
      <div v-if="op.type === 'addsign'" class="inline-form">
        <label>加签时机</label>
        <span class="radio-group">
          <label><input type="radio" value="AFTER" v-model="op.timing" /> 后置（默认 · 插到队尾）</label>
          <label><input type="radio" value="BEFORE" v-model="op.timing" /> 前置（插到当前办理人之前）</label>
        </span>
      </div>

      <div v-if="op.type === 'rollback'" class="inline-form">
        <label>回退目标节点（可选）</label>
        <input v-model="op.target_node_id" placeholder="node_id（留空＝上一节点）" />
      </div>

      <div v-if="op.type" class="inline-form">
        <label>原因</label>
        <input v-model="op.reason" placeholder="原因（建议必填）" @keyup.enter="doOp" />
        <!-- ★ 转交 / 加签须先选中办理人（防提交空 target）；回退 / 撤回无此要求 -->
        <button
          class="primary"
          :disabled="busy || (needsTarget && !op.target_open_id)"
          @click="doOp"
        >
          执行
        </button>
      </div>
    </div>

    <div v-if="detail" class="panel">
      <h2>操作留痕（时间线）</h2>
      <div v-if="!ops.length" class="empty">暂无记录</div>
      <ul v-else class="timeline">
        <li v-for="(o, idx) in ops" :key="idx">
          <div>
            <span class="tag info">{{ opTypeLabel(o.op_type) }}</span>
            <span class="muted"> · {{ fmtTime(o.created_at) }}</span>
          </div>
          <div class="muted">
            操作人：{{ o.actor_open_id || '-' }}
            <template v-if="o.from_status || o.to_status">
              · {{ statusLabel(o.from_status) }} → {{ statusLabel(o.to_status) }}
            </template>
          </div>
          <div v-if="o.reason">{{ o.reason }}</div>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
/* 办理人选择器（解红框）：可滚动点选列表，避免人数增多后 <select> 平铺不可用 */
.user-pick-list {
  max-height: 220px;
  overflow-y: auto;
  border: 1px solid var(--border);
  border-radius: 6px;
  margin: 8px 0;
}
.user-pick {
  list-style: none;
  margin: 0;
  padding: 0;
}
.user-pick li {
  padding: 6px 12px;
  cursor: pointer;
  border-bottom: 1px solid var(--border);
}
.user-pick li:last-child {
  border-bottom: none;
}
.user-pick li:hover {
  background: var(--bg, #f5f7fa);
}
.user-pick li.picked {
  background: var(--ok-bg, #e8f7ee);
  font-weight: 600;
}
</style>
