<script setup>
// 审批操作台（M9 · 转向 ③ 新增，契约见 docs/05-API.md §3.13）。
//
// 职责：
//   · 我方页面「同意 / 拒绝」两键（与飞书回调共用同一状态机出口，FR-M9-18）；
//   · 四操作：转交 / 加签 / 回退 / 撤回（飞书侧无这些按钮，全在我方页面完成）。
// ★ 加签（＝顺序会签）的「前置 / 后置」由**操作人当场选**（`timing ∈ {AFTER, BEFORE}`，
//   缺省 `AFTER`）—— 用户裁定，见 internal/flow/ops.go AddSignTiming / 01a §4.3。
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import {
  fetchApproval,
  approveTask,
  rejectTask,
  transferTask,
  addsignTask,
  rollbackTask,
  cancelInstance,
} from '../api'
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
}

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
    await load()
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}

function doApprove() {
  if (!requireTask()) return
  run(() => approveTask(bizNo, { task_id: selectedTaskId.value, opinion: opinion.value }), '已同意')
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
  // ★ 字段名对齐已实现的后端 handler（internal/httpapi/handlers_approval.go
  //   `approvalActionBody`）：目标 open_id ＝ `target`、回退目标节点 ＝ `target_node`。
  const base = { task_id: selectedTaskId.value, reason: op.reason }
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
              <td>{{ t.assignee_name || t.assignee_open_id || t.assignee || '-' }}</td>
              <td><span class="tag" :class="statusClass(t.status)">{{ statusLabel(t.status) }}</span></td>
              <td>{{ t.release_state || '-' }}</td>
              <td>{{ t.task_order != null ? t.task_order : '-' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="muted">★ 任一非终态实例恰 1 个可办理任务（顺序会签不变量）。请先在「节点」前选中要办理的任务。</p>
    </div>

    <div v-if="detail" class="panel">
      <h2>审批操作</h2>

      <div class="toolbar">
        <input v-model="opinion" placeholder="意见（可选）" @keyup.enter="doApprove" />
        <button class="primary" :disabled="busy || !actionable" @click="doApprove">同意</button>
        <button class="danger" :disabled="busy || !actionable" @click="doReject">拒绝</button>
      </div>
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

      <template v-if="op.type && op.type !== 'cancel'">
        <div class="inline-form">
          <label>办理人 open_id</label>
          <input v-model="op.target_open_id" placeholder="ou_xxx（转交 / 加签的目标）" />
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
        <button class="primary" :disabled="busy" @click="doOp">执行</button>
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
