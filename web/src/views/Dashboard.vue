<script setup>
// 看板（M5，FR-M5-01~08）：4 张看板 —— 13 预算执行 / 14 采购执行 / 15 费用结构 / 16 异常预警。
// 数据由服务端按角色施加「行级过滤（SQL 层）+ 列级投影（序列化层）」后返回；前端仅做展示。
// 看板只读，不提供任何写入口（FR-M5-06）。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts'
import { fetchDashboard, dashboardExportUrl } from '../api'

const DASHBOARDS = [
  { id: 13, key: 'budget', label: '预算执行' },
  { id: 14, key: 'purchase', label: '采购执行' },
  { id: 15, key: 'expense', label: '费用结构' },
  { id: 16, key: 'anomaly', label: '异常预警' },
]

const activeId = ref(14)
const period = ref(currentPeriod())
const loading = ref(false)
const err = ref('')
const data = ref(null)

// 图表实例：key → echarts 实例。
const chartEls = new Map()
const chartInstances = new Map()
const chartEmpty = ref({}) // key → true 表示无可用数值（列权限或无数据）

function currentPeriod() {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

const cards = computed(() => (data.value && Array.isArray(data.value.cards) ? data.value.cards : []))
const charts = computed(() => (data.value && Array.isArray(data.value.charts) ? data.value.charts : []))
const alerts = computed(() => (data.value && Array.isArray(data.value.alerts) ? data.value.alerts : []))
const supervision = computed(() => (data.value && data.value.supervision) || null)

// 预算看板（13）本期不启用：返回空序列。
const budgetEmpty = computed(() => activeId.value === 13)
const hasContent = computed(() => cards.value.length > 0 || charts.value.some((c) => (c.series || []).length > 0))

async function load() {
  loading.value = true
  err.value = ''
  try {
    data.value = await fetchDashboard(activeId.value, period.value)
  } catch (e) {
    data.value = null
    err.value = e.message || String(e)
  } finally {
    loading.value = false
    await nextTick()
    renderCharts()
  }
}

function setChartRef(key, el) {
  if (el) chartEls.set(key, el)
  else chartEls.delete(key)
}

// 数值口径：普通指标取 y；金额类指标取 amount_cents（分→元）。无权限列被服务端裁剪 → 返回 null。
function pointValue(p) {
  if (typeof p.y === 'number') return p.y
  if (typeof p.amount_cents === 'number') return Math.round(p.amount_cents) / 100
  return null
}

function disposeCharts() {
  for (const [, inst] of chartInstances) {
    try {
      inst.dispose()
    } catch (e) {
      /* ignore */
    }
  }
  chartInstances.clear()
}

function renderCharts() {
  disposeCharts()
  const empty = {}
  for (const ch of charts.value) {
    const el = chartEls.get(ch.key)
    if (!el) continue
    const series = Array.isArray(ch.series) ? ch.series : []
    const values = series.map(pointValue)
    if (series.length === 0 || values.every((v) => v === null)) {
      empty[ch.key] = true
      continue
    }
    empty[ch.key] = false
    const inst = echarts.init(el)
    chartInstances.set(ch.key, inst)
    inst.setOption(buildOption(ch, series, values))
  }
  chartEmpty.value = empty
  for (const [, inst] of chartInstances) inst.resize()
}

function buildOption(ch, series, values) {
  const categories = series.map((p) => p.x)
  const base = {
    tooltip: { trigger: ch.type === 'pie' ? 'item' : 'axis' },
    grid: { left: 48, right: 24, top: 30, bottom: 48 },
  }
  if (ch.type === 'pie') {
    return {
      ...base,
      series: [
        {
          type: 'pie',
          radius: '62%',
          data: categories.map((name, i) => ({ name, value: values[i] || 0 })),
          label: { formatter: '{b}: {c}' },
        },
      ],
    }
  }
  return {
    ...base,
    xAxis: { type: 'category', data: categories, axisLabel: { interval: 0, rotate: categories.length > 6 ? 30 : 0 } },
    yAxis: { type: 'value' },
    series: [
      {
        type: ch.type === 'line' ? 'line' : 'bar',
        data: values.map((v) => v || 0),
        smooth: ch.type === 'line',
        barMaxWidth: 40,
        itemStyle: { color: ch.key.includes('alert') ? '#f5222d' : '#1664ff', borderRadius: [4, 4, 0, 0] },
        areaStyle: ch.type === 'line' ? { opacity: 0.08 } : undefined,
      },
    ],
  }
}

function cardText(c) {
  if (c.amount_display !== undefined && c.amount_display !== null) return c.amount_display
  if (c.value !== undefined && c.value !== null) return c.value
  return '—'
}

function cardHint(c) {
  if (c.amount_display === undefined && c.amount_cents === undefined && c.value === undefined) {
    return '金额列对当前角色不可见'
  }
  return ''
}

function pct(ratio) {
  const n = typeof ratio === 'number' ? ratio : 0
  return `${(n * 100).toFixed(1)}%`
}

function alertClass(level) {
  return level === 'high' ? 'danger' : 'warn'
}

function exportUrl(format) {
  return dashboardExportUrl(activeId.value, period.value, format)
}

function onResize() {
  for (const [, inst] of chartInstances) inst.resize()
}

watch([activeId, period], () => {
  load()
})

onMounted(() => {
  load()
  window.addEventListener('resize', onResize)
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
  disposeCharts()
})
</script>

<template>
  <div>
    <div class="panel">
      <div class="toolbar">
        <button
          v-for="d in DASHBOARDS"
          :key="d.id"
          :class="['ghost', { primary: d.id === activeId }]"
          @click="activeId = d.id"
        >
          {{ d.label }}
        </button>
        <span style="margin-left: auto" class="muted">账期</span>
        <input v-model="period" type="month" />
        <a class="export-btn" :href="exportUrl('csv')">导出 CSV</a>
        <a class="export-btn" :href="exportUrl('xlsx')">导出 XLSX</a>
      </div>

      <div v-if="err" class="error">加载失败：{{ err }}</div>
      <div v-else-if="loading" class="empty">加载中…</div>
      <div v-else-if="budgetEmpty" class="empty">本期未启用（预算执行看板保留结构，暂不启用）</div>
      <div v-else-if="!hasContent" class="empty">暂无数据</div>
    </div>

    <template v-if="!loading && !err && !budgetEmpty">
      <!-- 指标卡 -->
      <div v-if="cards.length" class="panel">
        <h2>指标</h2>
        <div class="grid">
          <div v-for="c in cards" :key="c.key" class="stat">
            <div class="k">{{ c.label }}</div>
            <div class="v">{{ cardText(c) }}</div>
            <div v-if="cardHint(c)" class="muted" style="font-size: 12px; margin-top: 2px">
              {{ cardHint(c) }}
            </div>
          </div>
        </div>
      </div>

      <!-- 图表序列 -->
      <div v-for="ch in charts" :key="ch.key" class="panel">
        <h2>{{ ch.key }}</h2>
        <div v-if="chartEmpty[ch.key]" class="empty">无可用数值（金额列无权限或暂无数据）</div>
        <div v-show="!chartEmpty[ch.key]" :ref="(el) => setChartRef(ch.key, el)" class="chart"></div>
      </div>

      <!-- ★ 监督指标（FR-M5-07）：单独成项 -->
      <div v-if="supervision" class="panel">
        <h2>监督指标</h2>
        <div class="grid">
          <div class="stat">
            <div class="k">需求提出人任经办人的笔数（应恒为 0）</div>
            <div
              class="v"
              :style="{ color: supervision.requester_as_handler_count > 0 ? 'var(--danger)' : 'var(--ok)' }"
            >
              {{ supervision.requester_as_handler_count }}
            </div>
          </div>
        </div>
        <div style="margin-top: 12px">
          <div class="muted" style="margin-bottom: 6px">经办人指定集中度（某人被指定笔数 ÷ 总笔数）</div>
          <table v-if="supervision.handler_concentration && supervision.handler_concentration.length">
            <thead>
              <tr>
                <th>经办人</th>
                <th class="right">被指定笔数</th>
                <th class="right">集中度</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(h, i) in supervision.handler_concentration" :key="i">
                <td>{{ h.handler }}</td>
                <td class="right">{{ h.count }}</td>
                <td class="right">{{ pct(h.ratio) }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="muted">暂无指定经办记录</div>
        </div>
      </div>

      <!-- 异常预警面板（红标） -->
      <div v-if="alerts.length" class="panel alerts">
        <h2>异常预警（红标）</h2>
        <div class="grid">
          <div v-for="a in alerts" :key="a.key" class="stat" :class="alertClass(a.level)">
            <div class="k">{{ a.label || a.key }}</div>
            <div class="v">{{ a.count }}</div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.export-btn {
  display: inline-block;
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--primary);
}
.alerts .stat.danger {
  border-color: var(--danger);
  background: #fff1f0;
}
.alerts .stat.danger .v {
  color: var(--danger);
}
.alerts .stat.warn {
  border-color: var(--warn);
  background: #fff7e6;
}
.alerts .stat.warn .v {
  color: var(--warn);
}
</style>
