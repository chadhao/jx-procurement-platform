<script setup>
// 备付金（M1）：余额**只读**卡片 + 签领登记表单 + 月核销表单。
//
// ★ 制度口径：备付金不定额（有多少用多少、以实有金额为限）；按月凭单据核销，
//   余额以「核销后实有金额」为准；采一档时序为「先领款、后购买」。
// ★ 余额只读：本页无任何修改余额的入口（余额仅由「月核销」登记产生）。
import { onMounted, reactive, ref } from 'vue'
import { fetchPettyCashBalance, createPettyCashReceipt, monthlyClosePettyCash } from '../api-m1m6'

const err = ref('')
const msg = ref('')
const loading = ref(false)

// ---- 余额（只读）----
const period = ref(currentPeriod())
const balance = ref({ issued_cents: 0, spent_cents: 0, balance_cents: 0, as_of: '', found: false })

// ---- 签领登记 ----
const receipt = reactive({ biz_no: '', receiver_open_id: '', receiver_name: '', amount_yuan: '', received_date: '' })

// ---- 月核销 ----
const close = reactive({ period: currentPeriod(), issued_yuan: '', spent_yuan: '', balance_yuan: '', remark: '' })

function currentPeriod() {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

function fmt(cents) {
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

async function loadBalance() {
  err.value = ''
  try {
    const data = await fetchPettyCashBalance(period.value)
    balance.value = {
      issued_cents: data.issued_cents || 0,
      spent_cents: data.spent_cents || 0,
      balance_cents: data.balance_cents || 0,
      as_of: data.as_of || '',
      found: !!data.found,
    }
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function submitReceipt() {
  err.value = ''
  msg.value = ''
  const cents = toCents(receipt.amount_yuan)
  if (cents <= 0) {
    err.value = '签领金额必须为正数'
    return
  }
  if (!receipt.received_date) {
    err.value = '请填写签领日期'
    return
  }
  loading.value = true
  try {
    const data = await createPettyCashReceipt({
      biz_no: receipt.biz_no,
      receiver_open_id: receipt.receiver_open_id,
      receiver_name: receipt.receiver_name,
      amount_cents: cents,
      received_date: receipt.received_date,
    })
    msg.value = `签领登记成功（id=${data.id}）`
    receipt.biz_no = ''
    receipt.amount_yuan = ''
    receipt.received_date = ''
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

async function submitClose() {
  err.value = ''
  msg.value = ''
  const issued = toCents(close.issued_yuan)
  const spent = toCents(close.spent_yuan)
  const bal = toCents(close.balance_yuan)
  if (issued < 0 || spent < 0 || bal < 0) {
    err.value = '三项金额均为非负数'
    return
  }
  loading.value = true
  try {
    await monthlyClosePettyCash({
      period: close.period,
      issued_cents: issued,
      spent_cents: spent,
      balance_cents: bal,
      remark: close.remark,
    })
    msg.value = `账期 ${close.period} 核销登记成功`
    period.value = close.period
    await loadBalance()
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

onMounted(loadBalance)
</script>

<template>
  <div class="panel">
    <h2>备付金余额（只读）</h2>
    <div class="toolbar">
      <input v-model="period" placeholder="账期 YYYY-MM" @keyup.enter="loadBalance" />
      <button class="primary" @click="loadBalance">查询</button>
      <span class="muted">余额仅由「月核销」产生，本页无修改入口。</span>
    </div>
    <div class="grid">
      <div class="stat">
        <div class="k">当期签领 issued_cents</div>
        <div class="v">{{ fmt(balance.issued_cents) }}</div>
      </div>
      <div class="stat">
        <div class="k">当期支出 spent_cents</div>
        <div class="v">{{ fmt(balance.spent_cents) }}</div>
      </div>
      <div class="stat">
        <div class="k">核销后实有余额 balance_cents</div>
        <div class="v">{{ fmt(balance.balance_cents) }}</div>
      </div>
    </div>
    <p class="muted">
      as_of：{{ balance.as_of || '—' }}
      <span v-if="!balance.found">（该账期尚未核销，展示为 0）</span>
    </p>
  </div>

  <div class="panel">
    <h2>备付金签领登记（先领款、后购买）</h2>
    <div class="toolbar">
      <input v-model="receipt.biz_no" placeholder="关联报备单 biz_no（BA）" />
      <input v-model="receipt.receiver_open_id" placeholder="签领人 open_id" />
      <input v-model="receipt.receiver_name" placeholder="签领人姓名" />
      <input v-model="receipt.amount_yuan" placeholder="签领金额（元）" />
      <input v-model="receipt.received_date" placeholder="签领日期 YYYY-MM-DD" />
      <button class="primary" :disabled="loading" @click="submitReceipt">登记签领</button>
    </div>
  </div>

  <div class="panel">
    <h2>备付金月核销（唯一账期一条）</h2>
    <div class="toolbar">
      <input v-model="close.period" placeholder="账期 YYYY-MM" />
      <input v-model="close.issued_yuan" placeholder="当期签领（元）" />
      <input v-model="close.spent_yuan" placeholder="当期支出（元）" />
      <input v-model="close.balance_yuan" placeholder="核销后余额（元）" />
      <input v-model="close.remark" placeholder="备注" />
      <button class="primary" :disabled="loading" @click="submitClose">登记核销</button>
    </div>
    <p class="muted">同一账期重复核销将被拒绝（40900）。</p>
  </div>

  <div v-if="err" class="error">{{ err }}</div>
  <div v-if="msg" class="tag ok">{{ msg }}</div>
</template>
