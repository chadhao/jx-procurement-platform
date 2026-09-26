<script setup>
// 实例详情：基础信息 + 字段明细 + 状态时间线（追加式保留驳回→重提全链）。
import { onMounted, ref } from 'vue'
import { fetchInstance, fetchInstanceFields, fetchInstanceTimeline } from '../api'
import { statusClass, statusLabel, fmtTime } from '../utils'

const props = defineProps({ code: { type: String, required: true } })

const loading = ref(true)
const err = ref('')
const inst = ref(null)
const fields = ref([])
const timeline = ref([])

async function load() {
  loading.value = true
  err.value = ''
  try {
    const [i, f, t] = await Promise.all([
      fetchInstance(props.code),
      fetchInstanceFields(props.code).catch(() => ({ fields: [] })),
      fetchInstanceTimeline(props.code).catch(() => ({ events: [] })),
    ])
    inst.value = i
    fields.value = f.fields || []
    timeline.value = t.events || []
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="panel">
      <div class="toolbar">
        <router-link to="/instances">← 返回列表</router-link>
      </div>
      <div v-if="err" class="error">{{ err }}</div>
      <div v-else-if="loading" class="empty">加载中…</div>
      <template v-else-if="inst">
        <h2>实例 {{ inst.instance_code }}</h2>
        <dl class="kv">
          <dt>状态</dt>
          <dd><span class="tag" :class="statusClass(inst.status)">{{ statusLabel(inst.status) }}</span></dd>
          <dt>单据类型 doc_type</dt>
          <dd>{{ inst.doc_type || '-' }}</dd>
          <dt>业务单号 biz_no</dt>
          <dd>{{ inst.biz_no || '-' }}</dd>
          <dt>部门</dt>
          <dd>{{ inst.department || '-' }}</dd>
          <dt>申请人</dt>
          <dd>{{ (inst.applicant && (inst.applicant.name || inst.applicant.open_id)) || '-' }}</dd>
          <dt>金额</dt>
          <dd>{{ inst.amount_display || '-' }}</dd>
          <dt>来源</dt>
          <dd>{{ inst.source || '-' }}</dd>
          <dt>创建时间</dt>
          <dd>{{ fmtTime(inst.created_at) }}</dd>
          <dt>更新时间</dt>
          <dd>{{ fmtTime(inst.updated_at) }}</dd>
        </dl>
      </template>
    </div>

    <div class="panel">
      <h2>字段明细</h2>
      <div v-if="!fields.length" class="empty">暂无字段</div>
      <table v-else>
        <thead>
          <tr>
            <th>field_id</th>
            <th>字段名</th>
            <th>业务字段</th>
            <th>值</th>
            <th>类型</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(f, idx) in fields" :key="idx">
            <td>{{ f.field_id || '-' }}</td>
            <td>{{ f.field_name || '-' }}</td>
            <td>{{ f.biz_field || '-' }}</td>
            <td>{{ f.value || '-' }}</td>
            <td>{{ f.value_type || '-' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="panel">
      <h2>状态时间线</h2>
      <div v-if="!timeline.length" class="empty">暂无记录</div>
      <ul v-else class="timeline">
        <li v-for="(ev, idx) in timeline" :key="idx">
          <div>
            <span class="tag" :class="statusClass(ev.status)">{{ statusLabel(ev.status) }}</span>
            <span class="muted"> · #{{ ev.event_seq }} · {{ fmtTime(ev.occurred_at) }}</span>
          </div>
          <div class="muted">
            节点：{{ ev.task_node || '-' }} · 操作人：{{ ev.operator || '-' }}
          </div>
          <div v-if="ev.opinion">{{ ev.opinion }}</div>
        </li>
      </ul>
    </div>
  </div>
</template>
