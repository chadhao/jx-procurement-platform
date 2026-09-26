<script setup>
// 台账：按 table（ledger_type）查询；行·列权限由服务端裁剪，前端不感知被裁剪字段。
import { onMounted, reactive, ref } from 'vue'
import { fetchLedger, patchLedgerRow } from '../api'
import { fmtTime } from '../utils'

const loading = ref(false)
const err = ref('')
const msg = ref('')
const table = ref('purchase')
const items = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)
const scope = reactive({ biz_no: '', department: '', supplier: '', date_from: '', date_to: '' })

async function load() {
  loading.value = true
  err.value = ''
  msg.value = ''
  try {
    const data = await fetchLedger(table.value, {
      page: page.value,
      page_size: pageSize.value,
      biz_no: scope.biz_no,
      department: scope.department,
      supplier: scope.supplier,
      date_from: scope.date_from,
      date_to: scope.date_to,
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

// 写回运营字段（仅规则声明可写的字段会被服务端接受）
async function editOps(row) {
  const current = row.ops || {}
  const raw = window.prompt('运营字段（JSON）', JSON.stringify(current))
  if (raw === null) return
  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch (e) {
    err.value = 'JSON 解析失败：' + e.message
    return
  }
  try {
    await patchLedgerRow(table.value, row.id, parsed)
    msg.value = '已更新 ' + row.biz_no
    load()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

function columns() {
  const set = new Set()
  for (const r of items.value) {
    for (const k of Object.keys(r)) set.add(k)
  }
  // 去掉明细大字段与展示辅助列
  set.delete('archive')
  set.delete('ops')
  return Array.from(set)
}

onMounted(load)
</script>

<template>
  <div class="panel">
    <h2>台账</h2>
    <div class="toolbar">
      <select v-model="table" @change="search">
        <option value="purchase">采购台账 purchase</option>
        <option value="expense">费用台账 expense</option>
        <option value="petty_cash">备用金台账 petty_cash</option>
      </select>
      <input v-model="scope.biz_no" placeholder="biz_no" @keyup.enter="search" />
      <input v-model="scope.department" placeholder="department" @keyup.enter="search" />
      <input v-model="scope.supplier" placeholder="supplier" @keyup.enter="search" />
      <input v-model="scope.date_from" placeholder="起 YYYY-MM-DD" @keyup.enter="search" />
      <input v-model="scope.date_to" placeholder="止 YYYY-MM-DD" @keyup.enter="search" />
      <button class="primary" @click="search">查询</button>
    </div>

    <div v-if="err" class="error">{{ err }}</div>
    <div v-if="msg" class="tag ok">{{ msg }}</div>
    <div v-if="loading" class="empty">加载中…</div>
    <div v-else-if="!items.length" class="empty">暂无数据（或该台账对你的角色不可见）</div>
    <table v-else>
      <thead>
        <tr>
          <th v-for="c in columns()" :key="c">{{ c }}</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in items" :key="row.id">
          <td v-for="c in columns()" :key="c">{{ row[c] }}</td>
          <td><button class="ghost" @click="editOps(row)">写运营字段</button></td>
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
