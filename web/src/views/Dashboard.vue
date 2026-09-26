<script setup>
// 看板：实例状态分布 + 关键计数（数据源 /api/instances）。
import { onMounted, onBeforeUnmount, ref } from 'vue'
import * as echarts from 'echarts'
import { fetchInstances } from '../api'
import { statusLabel } from '../utils'

const loading = ref(true)
const err = ref('')
const items = ref([])
const chartEl = ref(null)
let chart = null

async function load() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchInstances({ page: 1, page_size: 200 })
    items.value = data.items || []
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
    render()
  }
}

function counts() {
  const c = {}
  for (const it of items.value) {
    const s = (it.status || 'UNKNOWN').toUpperCase()
    c[s] = (c[s] || 0) + 1
  }
  return c
}

function render() {
  if (!chartEl.value) return
  if (!chart) chart = echarts.init(chartEl.value)
  const c = counts()
  chart.setOption({
    tooltip: { trigger: 'axis' },
    grid: { left: 40, right: 20, top: 30, bottom: 40 },
    xAxis: { type: 'category', data: Object.keys(c).map(statusLabel) },
    yAxis: { type: 'value', minInterval: 1 },
    series: [
      {
        type: 'bar',
        data: Object.values(c),
        barMaxWidth: 48,
        itemStyle: { color: '#1664ff', borderRadius: [4, 4, 0, 0] },
      },
    ],
  })
  chart.resize()
}

function onResize() {
  if (chart) chart.resize()
}

onMounted(() => {
  load()
  window.addEventListener('resize', onResize)
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
  if (chart) chart.dispose()
  chart = null
})
</script>

<template>
  <div>
    <div class="panel">
      <h2>概览</h2>
      <div class="grid">
        <div class="stat">
          <div class="k">实例总数（样本）</div>
          <div class="v">{{ items.length }}</div>
        </div>
        <div class="stat">
          <div class="k">审批中</div>
          <div class="v">{{ counts().PENDING || 0 }}</div>
        </div>
        <div class="stat">
          <div class="k">已通过</div>
          <div class="v">{{ counts().APPROVED || 0 }}</div>
        </div>
        <div class="stat">
          <div class="k">已驳回</div>
          <div class="v">{{ counts().REJECTED || 0 }}</div>
        </div>
      </div>
    </div>

    <div class="panel">
      <h2>状态分布</h2>
      <div v-if="err" class="error">{{ err }}</div>
      <div v-else-if="loading" class="empty">加载中…</div>
      <div v-else-if="!items.length" class="empty">暂无数据</div>
      <div v-show="!loading && items.length" ref="chartEl" class="chart"></div>
    </div>
  </div>
</template>
