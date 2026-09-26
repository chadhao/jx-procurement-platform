<script setup>
// 审计日志：按操作人/角色/动作/资源/结果筛选。
import { onMounted, reactive, ref } from 'vue'
import { fetchAuditLogs } from '../api'
import { fmtTime } from '../utils'

const loading = ref(false)
const err = ref('')
const items = ref([])
const total = ref(0)
const offset = ref(0)
const pageSize = ref(50)
const filters = reactive({ actor: '', role: '', action: '', resource: '', result: '' })

async function load() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchAuditLogs({
      actor: filters.actor,
      role: filters.role,
      action: filters.action,
      resource: filters.resource,
      result: filters.result,
      page: 1,
      page_size: pageSize.value,
    })
    items.value = data.items || []
    total.value = data.total || 0
    offset.value = data.offset || 0
  } catch (e) {
    err.value = e.message || String(e)
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="panel">
    <h2>审计日志</h2>
    <div class="toolbar">
      <input v-model="filters.actor" placeholder="actor_open_id" @keyup.enter="load" />
      <input v-model="filters.role" placeholder="role" @keyup.enter="load" />
      <input v-model="filters.action" placeholder="action" @keyup.enter="load" />
      <input v-model="filters.resource" placeholder="resource" @keyup.enter="load" />
      <select v-model="filters.result">
        <option value="">全部结果</option>
        <option value="allow">allow</option>
        <option value="deny">deny</option>
      </select>
      <button class="primary" @click="load">查询</button>
    </div>

    <div v-if="err" class="error">{{ err }}</div>
    <div v-else-if="loading" class="empty">加载中…</div>
    <div v-else-if="!items.length" class="empty">暂无数据</div>
    <table v-else>
      <thead>
        <tr>
          <th>时间</th>
          <th>操作人</th>
          <th>角色</th>
          <th>动作</th>
          <th>资源</th>
          <th>对象</th>
          <th>结果</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(a, idx) in items" :key="idx">
          <td>{{ fmtTime(a.created_at) }}</td>
          <td>{{ a.actor_open_id || '-' }}</td>
          <td>{{ a.actor_role || '-' }}</td>
          <td>{{ a.action || '-' }}</td>
          <td>{{ a.resource || '-' }}</td>
          <td>{{ a.target_id || '-' }}</td>
          <td>
            <span class="tag" :class="a.result === 'deny' ? 'danger' : 'ok'">{{ a.result || '-' }}</span>
          </td>
        </tr>
      </tbody>
    </table>

    <div class="pager">
      <span>共 {{ total }} 条</span>
    </div>
  </div>
</template>
