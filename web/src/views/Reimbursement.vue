<script setup>
// 集团报销跟踪表（M1，FR-M1-02）——「审批外人工登记」。
//
// ★ 口径：报销**不进入本办法审批流程**（五条前提第 ④ 条：报销类可提前支出、公户转账一律走集团），
//   由人工走集团；本页承接「关联事前申请单号（SA）→ 初审 → 移交集团 → 集团付款」的登记与查询。
// ★ 集团侧付款字段（paid_date / paid_cents）**仅人工登记、不回填**（FR-M6-07 同源口径）。
// ★ 权限：写＝综合运营主管；读＝综合运营主管 / 主管领导 / 项目总经理 / 系统管理员（服务端二次校验，
//   此处的显示控制仅为导航提示，不构成安全边界）。
import { computed, onMounted, reactive, ref } from 'vue'
import { fetchReimbursements, createReimbursement, patchReimbursement } from '../api'
import { session } from '../store'

const err = ref('')
const msg = ref('')
const loading = ref(false)

const items = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)
const filters = reactive({ src_biz_no: '', department: '', review_state: '' })

// 登记表单
const form = reactive({
  src_biz_no: '',
  applicant_open_id: '',
  department: '',
  actual_yuan: '',
  invoice_count: '',
  review_state: '待初审',
  handover_date: '',
})

// 集团付款字段更新
const pay = reactive({ id: '', paid_date: '', paid_yuan: '', review_state: '集团已付款', overrun_note: '' })

const canWrite = computed(() => session.me && session.me.role === '综合运营主管')

const REVIEW_STATES = ['待初审', '初审通过', '初审退回', '已移交集团', '集团已付款']

function fmt(cents) {
  if (cents === undefined || cents === null) return '—'
  const neg = cents < 0
  const v = Math.abs(cents) / 100
  const s = v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  return (neg ? '-' : '') + s
}

function toCents(yuan) {
  const n = Number(yuan)
  if (!Number.isFinite(n) || n < 0) return -1
  return Math.round(n * 100)
}

async function load() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchReimbursements({
      page: page.value,
      page_size: pageSize.value,
      src_biz_no: filters.src_biz_no,
      department: filters.department,
      review_state: filters.review_state,
    })
    items.value = data.items || []
    total.value = data.total || 0
  } catch (e) {
    err.value = e.message || String(e)
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 1
  load()
}

async function submit() {
  err.value = ''
  msg.value = ''
  if (!form.src_biz_no.trim()) {
    err.value = '关联事前申请单号（src_biz_no）必填'
    return
  }
  const cents = toCents(form.actual_yuan)
  if (cents <= 0) {
    err.value = '实际金额必须为正数'
    return
  }
  loading.value = true
  try {
    const data = await createReimbursement({
      src_biz_no: form.src_biz_no.trim(),
      applicant_open_id: form.applicant_open_id,
      department: form.department,
      actual_cents: cents,
      invoice_count: form.invoice_count === '' ? 0 : Number(form.invoice_count),
      review_state: form.review_state,
      handover_date: form.handover_date,
    })
    msg.value = `登记成功（id=${data.id}，${data.amount_display}）`
    form.src_biz_no = ''
    form.actual_yuan = ''
    form.invoice_count = ''
    form.handover_date = ''
    await load()
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

function startPay(row) {
  pay.id = row.id
  pay.paid_date = row.paid_date || ''
  pay.paid_yuan = row.paid_cents != null ? String(row.paid_cents / 100) : ''
  pay.overrun_note = row.overrun_note || ''
}

async function submitPay() {
  err.value = ''
  msg.value = ''
  if (!pay.id) {
    err.value = '请先选择一条记录'
    return
  }
  const body = { review_state: pay.review_state }
  if (pay.paid_date) body.paid_date = pay.paid_date
  if (pay.paid_yuan !== '') {
    const cents = toCents(pay.paid_yuan)
    if (cents < 0) {
      err.value = '集团付款金额不得为负'
      return
    }
    body.paid_cents = cents
  }
  if (pay.overrun_note) body.overrun_note = pay.overrun_note
  loading.value = true
  try {
    await patchReimbursement(pay.id, body)
    msg.value = `已更新记录 ${pay.id}（集团侧字段为人工登记，不回填）`
    pay.id = ''
    await load()
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div v-if="canWrite" class="panel">
    <h2>集团报销跟踪 · 登记</h2>
    <div class="toolbar">
      <input v-model="form.src_biz_no" placeholder="关联事前申请单号 SA-YYMM-####" />
      <input v-model="form.applicant_open_id" placeholder="申请人 open_id" />
      <input v-model="form.department" placeholder="部门" />
      <input v-model="form.actual_yuan" placeholder="实际金额（元）" />
      <input v-model="form.invoice_count" placeholder="票据张数" />
      <select v-model="form.review_state">
        <option v-for="s in REVIEW_STATES" :key="s" :value="s">{{ s }}</option>
      </select>
      <input v-model="form.handover_date" placeholder="移交集团日期 YYYY-MM-DD" />
      <button class="primary" :disabled="loading" @click="submit">登记</button>
    </div>
    <p class="muted">报销不进入本办法审批流程（人工走集团）；本表仅作审批外登记，供台账与看板引用。</p>
  </div>

  <div class="panel">
    <h2>集团报销跟踪 · 查询</h2>
    <div class="toolbar">
      <input v-model="filters.src_biz_no" placeholder="src_biz_no" @keyup.enter="search" />
      <input v-model="filters.department" placeholder="department" @keyup.enter="search" />
      <select v-model="filters.review_state" @change="search">
        <option value="">全部初审状态</option>
        <option v-for="s in REVIEW_STATES" :key="s" :value="s">{{ s }}</option>
      </select>
      <button class="primary" @click="search">查询</button>
    </div>

    <div v-if="err" class="error">{{ err }}</div>
    <div v-if="msg" class="tag ok">{{ msg }}</div>
    <div v-if="loading" class="empty">加载中…</div>
    <div v-else-if="!items.length" class="empty">暂无记录</div>
    <table v-else>
      <thead>
        <tr>
          <th>关联单号</th>
          <th>申请人</th>
          <th>部门</th>
          <th>实际金额</th>
          <th>票据张数</th>
          <th>初审状态</th>
          <th>移交日期</th>
          <th>集团付款日期</th>
          <th>集团付款金额</th>
          <th>来源</th>
          <th v-if="canWrite">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in items" :key="row.id">
          <td>{{ row.src_biz_no }}</td>
          <td>{{ row.applicant }}</td>
          <td>{{ row.department }}</td>
          <td>{{ row.amount_display || fmt(row.actual_cents) }}</td>
          <td>{{ row.invoice_count }}</td>
          <td>{{ row.review_state || '—' }}</td>
          <td>{{ row.handover_date || '—' }}</td>
          <td>{{ row.paid_date || '—' }}</td>
          <td>{{ row.paid_display || '—' }}</td>
          <td>{{ row.source }}</td>
          <td v-if="canWrite"><button class="ghost" @click="startPay(row)">更新集团付款</button></td>
        </tr>
      </tbody>
    </table>

    <div class="pager">
      <span>共 {{ total }} 条 · 第 {{ page }} 页</span>
      <button class="ghost" :disabled="page <= 1" @click="page -= 1; load()">上一页</button>
      <button class="ghost" :disabled="page * pageSize >= total" @click="page += 1; load()">下一页</button>
    </div>
  </div>

  <div v-if="canWrite" class="panel">
    <h2>集团付款字段 · 人工登记</h2>
    <div class="toolbar">
      <input v-model="pay.id" placeholder="记录 id" />
      <input v-model="pay.paid_date" placeholder="集团付款日期 YYYY-MM-DD" />
      <input v-model="pay.paid_yuan" placeholder="集团付款金额（元）" />
      <select v-model="pay.review_state">
        <option v-for="s in REVIEW_STATES" :key="s" :value="s">{{ s }}</option>
      </select>
      <input v-model="pay.overrun_note" placeholder="超支说明" />
      <button class="primary" :disabled="loading" @click="submitPay">更新</button>
    </div>
    <p class="muted">集团侧字段只接受人工传入值，系统不做任何派生或回填。</p>
  </div>
</template>
