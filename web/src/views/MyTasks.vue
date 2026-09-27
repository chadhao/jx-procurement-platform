<script setup>
// 我的待办（M9 · 转向 ③ 新增，契约见 docs/05-API.md §3.13）。
//
// 数据源：GET /api/approval/tasks —— t_flow_task 中 RELEASED ∧ PENDING ∧ assignee=me。
// ★ 不变量（04a §2.3 顺序会签）：任一「非终态实例」的可办理任务恰 1 个。
//   故本页每行对应「一件我此刻能办的活」，点「办理」即进审批操作台。
import { onMounted, ref } from 'vue'
import { fetchMyTasks } from '../api'

const loading = ref(false)
const err = ref('')
const items = ref([])

async function load() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchMyTasks()
    items.value = data.items || []
  } catch (e) {
    err.value = e.message || String(e)
    items.value = []
  } finally {
    loading.value = false
  }
}

function amountText(it) {
  if (it.amount_display) return it.amount_display
  if (it.amount_cents != null) return (it.amount_cents / 100).toFixed(2)
  if (it.amount != null && it.amount !== '') return it.amount
  return '-'
}

onMounted(load)
</script>

<template>
  <div class="panel">
    <h2>我的待办</h2>
    <p class="muted">任一「非终态实例」的可办理任务恰 1 个（顺序会签不变量）。</p>

    <div class="toolbar">
      <button class="ghost" @click="load">刷新</button>
    </div>

    <div v-if="err" class="error">{{ err }}</div>
    <div v-else-if="loading" class="empty">加载中…</div>
    <div v-else-if="!items.length" class="empty">暂无待办</div>

    <div v-else class="table-scroll">
      <table>
        <thead>
          <tr>
            <th>biz_no</th>
            <th>单据类型</th>
            <th>当前节点</th>
            <th>申请人</th>
            <th class="right">金额</th>
            <th>顺序</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="it in items" :key="it.task_id || it.biz_no">
            <td>
              <router-link :to="`/approval/${encodeURIComponent(it.biz_no)}?task_id=${encodeURIComponent(it.task_id || '')}`">
                {{ it.biz_no }}
              </router-link>
            </td>
            <td>{{ it.doc_type || '-' }}</td>
            <td>{{ it.node_name || '-' }}</td>
            <td>{{ it.applicant || '-' }}</td>
            <td class="right">{{ amountText(it) }}</td>
            <td>{{ it.task_order != null ? it.task_order : '-' }}</td>
            <td>
              <router-link
                :to="`/approval/${encodeURIComponent(it.biz_no)}?task_id=${encodeURIComponent(it.task_id || '')}`"
              >
                <button class="primary">办理</button>
              </router-link>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
