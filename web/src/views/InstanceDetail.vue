<script setup>
// 实例详情：基础信息 + 状态时间线（追加式保留驳回→重提全链）。
//
// ★ R12 语义对齐（转向 ③）：
//   ① `GET /api/instances/{code}/fields` **已作废（F3）** —— 原生控件链作废、恒空、再无生产者；
//      表单字段改由我方提交页 / 三方审批定义持有，故本页**移除字段明细面板**，不再依赖该端点。
//   ② 审批流转（同意/拒绝/四操作）与细粒度「操作留痕（ops）」在「审批操作台」（§3.13），
//      本页以其 `biz_no` 为入口。
import { onMounted, ref } from 'vue'
import { fetchInstance, fetchInstanceTimeline } from '../api'
import { statusClass, statusLabel, fmtTime } from '../utils'

const props = defineProps({ code: { type: String, required: true } })

const loading = ref(true)
const err = ref('')
const inst = ref(null)
const timeline = ref([])

async function load() {
  loading.value = true
  err.value = ''
  try {
    const [i, t] = await Promise.all([
      fetchInstance(props.code),
      fetchInstanceTimeline(props.code).catch(() => ({ events: [] })),
    ])
    inst.value = i
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
        <router-link
          v-if="inst && inst.biz_no"
          class="link-btn"
          :to="`/approval/${encodeURIComponent(inst.biz_no)}`"
        >
          进入审批操作台
        </router-link>
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

    <!-- ★ 字段明细：端点 GET /api/instances/{code}/fields 已随转向 ③ 作废（F3），不再渲染。 -->
    <div class="panel">
      <h2>字段明细</h2>
      <p class="muted">
        本面板已作废（转向 ③ · 作废项 F3）：三方审批定义<b>不含原生表单控件</b>，表单字段改由
        <b>我方提交页</b> / 三方审批定义持有，旧端点恒空、不再有生产者。
      </p>
    </div>

    <div class="panel">
      <h2>状态史</h2>
      <p class="muted">状态变更史（含驳回→重提完整链条）。逐操作轨迹见「审批操作台」的「操作留痕」。</p>
      <div v-if="!timeline.length" class="empty">暂无记录</div>
      <ul v-else class="timeline">
        <li v-for="(ev, idx) in timeline" :key="idx">
          <div>
            <span class="tag" :class="statusClass(ev.status)">{{ statusLabel(ev.status) }}</span>
            <span class="muted"> · #{{ ev.event_seq }} · {{ fmtTime(ev.occurred_at) }}</span>
          </div>
          <div class="muted">
            节点：{{ ev.task_node || '—（本期恒空，见 §3.4）' }} · 操作人：{{ ev.operator || '-' }}
          </div>
          <div v-if="ev.opinion">{{ ev.opinion }}</div>
        </li>
      </ul>
    </div>
  </div>
</template>
