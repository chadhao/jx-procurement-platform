<script setup>
// 审批实例列表：筛选 + 分页 + 行过滤（服务端强制，前端仅展示可见行）。
import { onMounted, reactive, ref } from 'vue'
import { fetchInstances } from '../api'
import { statusClass, statusLabel, fmtTime } from '../utils'

const loading = ref(false)
const err = ref('')
const items = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)

const filters = reactive({
  approval_code: '',
  doc_type: '',
  status: '',
  department: '',
})

async function load() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchInstances({
      page: page.value,
      page_size: pageSize.value,
      approval_code: filters.approval_code,
      doc_type: filters.doc_type,
      status: filters.status,
      department: filters.department,
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

onMounted(load)
</script>

<template>
  <div class="panel">
    <h2>审批实例</h2>
    <div class="toolbar">
      <input v-model="filters.approval_code" placeholder="approval_code" @keyup.enter="search" />
      <input v-model="filters.doc_type" placeholder="doc_type" @keyup.enter="search" />
      <select v-model="filters.status">
        <option value="">全部状态</option>
        <option value="PENDING">审批中</option>
        <option value="APPROVED">已通过</option>
        <option value="REJECTED">已驳回</option>
        <option value="CANCELED">已撤回</option>
        <option value="OVERTIME_CLOSE">超时关闭</option>
      </select>
      <input v-model="filters.department" placeholder="department" @keyup.enter="search" />
      <button class="primary" @click="search">查询</button>
    </div>

    <div v-if="err" class="error">{{ err }}</div>
    <div v-else-if="loading" class="empty">加载中…</div>
    <div v-else-if="!items.length" class="empty">暂无数据</div>
    <table v-else>
      <thead>
        <tr>
          <th>instance_code</th>
          <th>单据类型</th>
          <th>biz_no</th>
          <th>状态</th>
          <th>部门</th>
          <th class="right">金额</th>
          <th>来源</th>
          <th>更新时间</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="it in items" :key="it.instance_code">
          <td>
            <router-link :to="`/instances/${encodeURIComponent(it.instance_code)}`">
              {{ it.instance_code }}
            </router-link>
          </td>
          <td>{{ it.doc_type || '-' }}</td>
          <td>{{ it.biz_no || '-' }}</td>
          <td><span class="tag" :class="statusClass(it.status)">{{ statusLabel(it.status) }}</span></td>
          <td>{{ it.department || '-' }}</td>
          <td class="right">{{ it.amount_display || '-' }}</td>
          <td>{{ it.source || '-' }}</td>
          <td>{{ fmtTime(it.updated_at) }}</td>
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
