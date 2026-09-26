<script setup>
// 台账：按 table（ledger_type，L01..L12）查询；行·列权限由服务端裁剪，前端不感知被裁剪字段。
import { computed, onMounted, reactive, ref } from 'vue'
import { fetchLedger, patchLedgerRow, fetchContractChanges } from '../api'
import { fmtTime } from '../utils'
import { LEDGER_TYPES, DEFAULT_LEDGER_TYPE, ledgerName, isReadOnlyLedger } from '../ledgerTypes'

const loading = ref(false)
const err = ref('')
const msg = ref('')
// ★ 台账类型键必须与后端 ledger_type 一致（L01..L12）；此前用 purchase/expense/petty_cash 会恒空。
const table = ref(DEFAULT_LEDGER_TYPE)
const items = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)
const scope = reactive({ biz_no: '', department: '', supplier: '', date_from: '', date_to: '' })

// 变更链（FR-M4-07）：按合同号回溯历次变更的次数 / 累计金额 / 所取档位。
const chain = reactive({ open: false, loading: false, err: '', contractNo: '', data: null })

async function openChain(row) {
  chain.open = true
  chain.loading = true
  chain.err = ''
  chain.data = null
  chain.contractNo = row.biz_no
  try {
    chain.data = await fetchContractChanges(row.biz_no)
  } catch (e) {
    chain.err = e.message || String(e)
  } finally {
    chain.loading = false
  }
}

function closeChain() {
  chain.open = false
  chain.data = null
  chain.err = ''
}

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
    derived.value = data.derived === true
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

// 无写入口的台账（派生视图 / 只读汇总 / 本期未启用）——与后端 config.ReadOnlyLedgerTypes 同口径。
const readOnly = computed(() => isReadOnlyLedger(table.value))
// 派生视图（当前仅 L11）：行由查询时聚合产生，**无 id**，故不能按 id 读/写。
const derived = ref(false)
// 行键：派生行没有 id，退用 biz_no —— 否则 v-for :key 全为 undefined（Vue 会报重复 key 且渲染异常）。
function rowKey(row) {
  return row.id != null ? 'id-' + row.id : 'bn-' + (row.biz_no || '')
}
// 仅"有 id 且台账非只读"时才允许写运营字段。
function canEdit(row) {
  return !readOnly.value && row.id != null
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
        <option v-for="t in LEDGER_TYPES" :key="t.key" :value="t.key">
          {{ t.name }} {{ t.key }}
        </option>
      </select>
      <input v-model="scope.biz_no" placeholder="biz_no" @keyup.enter="search" />
      <input v-model="scope.department" placeholder="department" @keyup.enter="search" />
      <input v-model="scope.supplier" placeholder="supplier" @keyup.enter="search" />
      <input v-model="scope.date_from" placeholder="起 YYYY-MM-DD" @keyup.enter="search" />
      <input v-model="scope.date_to" placeholder="止 YYYY-MM-DD" @keyup.enter="search" />
      <button class="primary" @click="search">查询</button>
    </div>
    <p class="muted" style="margin: 4px 0 10px">
      当前：{{ ledgerName(table) }}（{{ table }}）
      <span v-if="derived"> · 派生视图（由合同台账 + 到货验收聚合，不落行、不可写）</span>
      <span v-else-if="readOnly"> · 只读（无写入口）</span>
    </p>

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
        <tr v-for="row in items" :key="rowKey(row)">
          <td v-for="c in columns()" :key="c">{{ row[c] }}</td>
          <td>
            <button v-if="canEdit(row)" class="ghost" @click="editOps(row)">写运营字段</button>
            <span v-else class="muted">只读</span>
            <button class="ghost" @click="openChain(row)">变更链</button>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="chain.open" class="panel" style="margin-top: 14px">
      <h2>
        变更链回溯 · {{ chain.contractNo }}
        <button class="ghost" style="float: right" @click="closeChain">关闭</button>
      </h2>
      <div v-if="chain.err" class="error">{{ chain.err }}</div>
      <div v-else-if="chain.loading" class="empty">加载中…</div>
      <template v-else-if="chain.data">
        <div class="grid">
          <div class="stat">
            <div class="k">历次变更次数</div>
            <div class="v">{{ chain.data.count }}</div>
          </div>
          <div class="stat">
            <div class="k">累计变更金额</div>
            <div class="v">
              {{ chain.data.cumulative_change_display || (chain.data.amount_hidden ? '金额列不可见' : '—') }}
            </div>
          </div>
        </div>
        <div v-if="!chain.data.items || !chain.data.items.length" class="empty">该合同号下无变更记录</div>
        <table v-else style="margin-top: 10px">
          <thead>
            <tr>
              <th>变更单号</th>
              <th>变更差额</th>
              <th>原合同金额</th>
              <th>取用档位</th>
              <th>业务日期</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="it in chain.data.items" :key="it.biz_no">
              <td>{{ it.biz_no }}</td>
              <td>{{ it.change_display || '—' }}</td>
              <td>{{ it.original_display || '—' }}</td>
              <td>{{ it.tier || '—' }}</td>
              <td>{{ it.biz_date || '—' }}</td>
            </tr>
          </tbody>
        </table>
        <p class="muted" style="margin-top: 8px">
          档位口径：变更审批档位 = max（变更差额, 原合同金额），化整为零通道已关闭。
        </p>
      </template>
    </div>

    <div class="pager">
      <span>共 {{ total }} 条 · 第 {{ page }} 页</span>
      <button class="ghost" :disabled="page <= 1" @click="prev">上一页</button>
      <button class="ghost" :disabled="page * pageSize >= total" @click="next">下一页</button>
    </div>
  </div>
</template>
