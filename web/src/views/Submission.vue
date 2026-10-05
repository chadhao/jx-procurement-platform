<script setup>
// 报送与凭证包（M6）：列表 + 状态 / 3 个工作日超期红标 + 「已提交未付款」筛选 +
// 新建报送 + 一键导出凭证包（zip/pdf）+ 移交凭证登记 + 集团侧人工登记 + 驳回处置登记。
//
// ★ 「无凭证视为未提交」：无移交凭证时状态判为「未提交」，不得计入「已提交未付款」清单。
// ★ 集团侧字段（受理编号 / 流程状态 / 付款完成日期）仅人工登记、不回填（来源标注「人工登记」）。
import { onMounted, reactive, ref } from 'vue'
import {
  fetchSubmissions,
  createSubmission,
  genIdempotencyKey,
  registerSubmissionReceipt,
  registerSubmissionGroup,
  registerSubmissionReject,
  submissionPackageUrl,
} from '../api-m1m6'

const err = ref('')
const msg = ref('')
const loading = ref(false)
const submitting = ref(false)

// ★ 幂等键按「登记意图」持有：首次提交时生成，成功后轮换；失败则保留，
//   使重复点击 / 网络重试复用同一个键，服务端据此复用首次结果而非重复落库。
const idemKey = ref('')
function currentIdemKey() {
  if (!idemKey.value) idemKey.value = genIdempotencyKey()
  return idemKey.value
}

const filter = reactive({ state: '', period: '', overdue: false })
const items = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)

const showCreate = ref(false)
const form = reactive({
  biz_no: '',
  subject_type: '',
  amount_yuan: '',
  pay_method: '',
  hn_finish_date: '',
  submit_date: '',
  receipt_ref: '',
  items_text: '',
})

const STATES = ['未提交', '已提交', '办理中', '已付款', '已驳回']

function fmt(cents) {
  if (cents === null || cents === undefined) return '—'
  return (cents / 100).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

function stateTag(state) {
  if (state === '已付款') return 'ok'
  if (state === '已驳回') return 'danger'
  if (state === '未提交') return 'warn'
  if (state === '已提交' || state === '办理中') return 'info'
  return ''
}

function parseItems(text) {
  const out = []
  for (const line of String(text || '').split('\n')) {
    const t = line.trim()
    if (!t) continue
    const [no, type] = t.split(/[,，\s]+/)
    if (no) out.push({ item_biz_no: no, item_type: type || '' })
  }
  return out
}

async function load() {
  loading.value = true
  err.value = ''
  try {
    const params = { page: page.value, page_size: pageSize.value, state: filter.state, period: filter.period }
    if (filter.overdue) params.overdue = 'true'
    const data = await fetchSubmissions(params)
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

// exportListCsv N-060 F11（FR-M6-04）：当前筛选的**全量**清单导出 CSV（仅筛选 ⇒ 补导出）。
// 以 page_size=10000 重拉（不只当前页）；BOM 前缀（Excel 中文不乱码）；列＝首行动态键。
async function exportListCsv() {
  try {
    const params = { page: 1, page_size: 10000, state: filter.state, period: filter.period }
    if (filter.overdue) params.overdue = 'true'
    const data = await fetchSubmissions(params)
    const rows = data.items || []
    if (!rows.length) {
      err.value = '当前筛选无可导出行'
      return
    }
    const cols = Object.keys(rows[0])
    const esc = (v) => {
      const s2 = v == null ? '' : String(v)
      return /[",\n]/.test(s2) ? '"' + s2.replace(/"/g, '""') + '"' : s2
    }
    const csv = [cols.join(',')]
      .concat(rows.map((r) => cols.map((c) => esc(r[c])).join(',')))
      .join('\r\n')
    const blob = new window.Blob(['\uFEFF' + csv], { type: 'text/csv;charset=utf-8' })
    const a = document.createElement('a')
    a.href = window.URL.createObjectURL(blob)
    a.download = `报送清单_${filter.period || 'all'}_${filter.state || '全状态'}.csv`
    a.click()
    window.URL.revokeObjectURL(a.href)
    msg.value = `已导出 ${rows.length} 行`
    err.value = ''
  } catch (e) {
    err.value = e.message || String(e)
  }
}

function quickSubmittedUnpaid() {
  filter.state = '已提交'
  filter.overdue = false
  search()
}

async function submitCreate() {
  err.value = ''
  msg.value = ''
  if (submitting.value) return // 本地防抖（服务端幂等为最终保障）
  if (!String(form.biz_no || '').trim()) {
    err.value = '业务单号不能为空（它是报送记录的业务唯一键，也是去重锚点）'
    return
  }
  if (!form.subject_type) {
    err.value = '事项类型不能为空'
    return
  }
  const payload = {
    biz_no: form.biz_no,
    subject_type: form.subject_type,
    pay_method: form.pay_method,
    hn_finish_date: form.hn_finish_date,
    submit_date: form.submit_date,
    receipt_ref: form.receipt_ref,
    items: parseItems(form.items_text),
  }
  if (form.amount_yuan !== '') {
    const n = Number(form.amount_yuan)
    if (!Number.isFinite(n) || n < 0) {
      err.value = '金额必须为非负数'
      return
    }
    payload.amount_cents = Math.round(n * 100)
  }
  submitting.value = true
  try {
    // ★ 复用当前登记意图的幂等键（失败保留、成功轮换）。
    const data = await createSubmission(payload, currentIdemKey())
    msg.value = data.idempotent_replay
      ? `该请求此前已登记，返回原有记录（id=${data.id}，状态=${data.submit_state}）`
      : `报送登记成功（id=${data.id}，状态=${data.submit_state}）`
    idemKey.value = '' // 本次意图完成，下次登记使用新键
    showCreate.value = false
    Object.assign(form, {
      biz_no: '', subject_type: '', amount_yuan: '', pay_method: '',
      hn_finish_date: '', submit_date: '', receipt_ref: '', items_text: '',
    })
    search()
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    submitting.value = false
  }
}

async function registerReceipt(row) {
  const ref = window.prompt('登记移交凭证（签收记录）号：', row.receipt_ref || '')
  if (ref === null) return
  try {
    await registerSubmissionReceipt(row.id, ref)
    msg.value = '移交凭证已登记，状态更新为已提交'
    load()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function registerGroup(row) {
  const raw = window.prompt(
    '集团侧字段人工登记（JSON，仅人工登记、不回填）：',
    JSON.stringify({ grp_accept_no: row.group?.accept_no || '', grp_state: row.group?.state || '', paid_date: row.group?.paid_date || '' }),
  )
  if (raw === null) return
  let payload
  try {
    payload = JSON.parse(raw)
  } catch (e) {
    err.value = 'JSON 解析失败：' + e.message
    return
  }
  try {
    await registerSubmissionGroup(row.id, payload)
    msg.value = '集团侧字段已人工登记'
    load()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function registerReject(row) {
  const action = window.prompt('集团驳回处置方式（取消 / 驳回重走）：', '驳回重走')
  if (action === null) return
  const reason = window.prompt('驳回原因：', '')
  if (reason === null) return
  try {
    await registerSubmissionReject(row.id, { action, reason })
    msg.value = '驳回处置已登记'
    load()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

function exportPackage(row, format) {
  window.open(submissionPackageUrl(row.id, format), '_blank')
}

function prev() {
  if (page.value > 1) {
    page.value -= 1
    load()
  }
}

function next() {
  if (page.value * pageSize.value < total.value) {
    page.value += 1
    load()
  }
}

onMounted(load)
</script>

<template>
  <div class="panel">
    <h2>报送与凭证包（M6）</h2>
    <div class="toolbar">
      <select v-model="filter.state" @change="search">
        <option value="">全部状态</option>
        <option v-for="s in STATES" :key="s" :value="s">{{ s }}</option>
      </select>
      <input v-model="filter.period" placeholder="账期 YYYY-MM" @keyup.enter="search" />
      <label class="muted"><input type="checkbox" v-model="filter.overdue" @change="search" /> 仅超期（3 个工作日）</label>
      <button class="primary" @click="search">查询</button>
      <button class="ghost" @click="quickSubmittedUnpaid">已提交未付款</button>
      <button class="ghost" @click="exportListCsv">导出清单 CSV</button>
      <button class="ghost" @click="showCreate = !showCreate">新建报送</button>
    </div>

    <div v-if="showCreate" class="toolbar">
      <input v-model="form.biz_no" placeholder="业务单号 SUB-YYMM-####（必填）" />
      <input v-model="form.subject_type" placeholder="事项类型" />
      <input v-model="form.amount_yuan" placeholder="金额（元）" />
      <input v-model="form.pay_method" placeholder="付款方式" />
      <input v-model="form.hn_finish_date" placeholder="湖南侧完成日期 YYYY-MM-DD" />
      <input v-model="form.submit_date" placeholder="提交集团日期 YYYY-MM-DD" />
      <input v-model="form.receipt_ref" placeholder="移交凭证号（留空=未提交）" />
      <button class="primary" :disabled="submitting" @click="submitCreate">
        {{ submitting ? '提交中…' : '提交登记' }}
      </button>
    </div>
    <div v-if="showCreate" class="toolbar">
      <textarea
        v-model="form.items_text"
        rows="3"
        style="width: 100%"
        placeholder="关联单据清单：每行一笔，格式「单据编号,类型」（如 PR-2609-0001,PR）"
      ></textarea>
    </div>

    <div v-if="err" class="error">{{ err }}</div>
    <div v-if="msg" class="tag ok">{{ msg }}</div>
    <div v-if="loading" class="empty">加载中…</div>
    <div v-else-if="!items.length" class="empty">暂无报送记录</div>
    <table v-else>
      <thead>
        <tr>
          <th>id</th>
          <th>业务单号</th>
          <th>事项类型</th>
          <th>金额</th>
          <th>状态</th>
          <th>超期</th>
          <th>湖南侧完成</th>
          <th>提交日期</th>
          <th>移交凭证</th>
          <th>集团侧（人工登记）</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in items" :key="row.id">
          <td>{{ row.id }}</td>
          <td>{{ row.biz_no || '—' }}</td>
          <td>{{ row.subject_type }}</td>
          <td class="right">{{ fmt(row.amount_cents) }}</td>
          <td><span class="tag" :class="stateTag(row.submit_state)">{{ row.submit_state }}</span></td>
          <td>
            <span v-if="row.overdue" class="tag danger">超期</span>
            <span v-else class="muted">—</span>
          </td>
          <td>{{ row.hn_finish_date || '—' }}</td>
          <td>{{ row.submit_date || '—' }}</td>
          <td>{{ row.receipt_ref || '—' }}</td>
          <td class="muted">
            受理:{{ row.group?.accept_no || '—' }} / 状态:{{ row.group?.state || '—' }} / 付款:{{ row.group?.paid_date || '—' }}
          </td>
          <td>
            <button class="ghost" @click="exportPackage(row, 'zip')">导出 zip</button>
            <button class="ghost" @click="exportPackage(row, 'pdf')">PDF</button>
            <button class="ghost" @click="registerReceipt(row)">登记凭证</button>
            <button class="ghost" @click="registerGroup(row)">集团登记</button>
            <button class="ghost" @click="registerReject(row)">驳回处置</button>
          </td>
        </tr>
      </tbody>
    </table>

    <div class="pager">
      <span>共 {{ total }} 条 · 第 {{ page }} 页</span>
      <button class="ghost" :disabled="page <= 1" @click="prev">上一页</button>
      <button class="ghost" :disabled="page * pageSize >= total" @click="next">下一页</button>
    </div>
  </div>
</template>
