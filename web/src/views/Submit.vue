<script setup>
// 发起申请（M7 · 批 1）：meta 驱动表单 + 分档/链预览 + 提交。
//
// ★ D1/N-008 定案：表单 schema **后端权威**（spec/forms/*.json → GET /api/approval/meta），
//   本页只做渲染与即时提示，不持任何业务口径（校验以服务端为准）。
// ★ D5：分档/审批链预览走 POST /api/approval/preview —— 与提交**共算同一入口**，
//   前端不本地算分档；unresolved_roles 非空 ⇒ 提交将被阻断，预览先行暴露（R-g 缓解）。
// ★ 提交携带 Idempotency-Key（d9）：本页会话内固定一把，重复点击/网络重试不重复建单。
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  fetchApprovalMeta, previewApproval, submitApproval, uploadApprovalAttachment,
} from '../api'

const props = defineProps({ docType: { type: String, default: '' } })
const route = useRoute()
const router = useRouter()

const err = ref('')
const msg = ref('')
const loading = ref(false)
const meta = ref(null)

const curDocType = ref(props.docType || '')
const fields = reactive({})        // 字段名 → 原始值（money 字段暂存「元」字符串）
const attachments = ref([])        // [{file_id, file_name}]
const uploading = ref(false)
const preview = ref(null)
const previewHint = ref('')        // 预览期的可见提示（如 PR<1000 引导）
const idemKey = ref(genIdemKey())

let previewTimer = null

// ---- meta ----
const forms = computed(() => (meta.value && meta.value.forms) || [])
const curForm = computed(() => forms.value.find((f) => f.doc_type === curDocType.value) || null)
const approvalCode = computed(() => {
  const codes = (meta.value && meta.value.approval_codes) || {}
  return codes[curDocType.value] || ''
})

// 提交时点字段：filled_at 为空的 section、source=user 的字段（与服务端校验口径一致）。
const visibleFields = computed(() => {
  const f = curForm.value
  if (!f) return []
  const out = []
  for (const sec of f.sections || []) {
    if (sec.filled_at) continue // 审批/拨付/后置/周期 section 不在提交时点渲染
    for (const field of sec.fields || []) {
      if (field.source === 'user') out.push({ ...field, sectionLabel: sec.label })
    }
  }
  return out
})

// ---- 用途分类联动（enums 权威：groups[].categories[].code/label/secondary）----
const usageGroups = computed(() => {
  const uc = meta.value && meta.value.enums && meta.value.enums.usage_categories
  return (uc && uc.groups) || []
})
const usageL1Options = computed(() => {
  const out = []
  for (const g of usageGroups.value) {
    for (const c of g.categories || []) out.push({ code: c.code, label: `${c.code} ${c.label}` })
  }
  return out
})
const usageL2Options = computed(() => {
  const l1 = fields.usage_category_l1
  for (const g of usageGroups.value) {
    for (const c of g.categories || []) {
      if (c.code === l1) return c.secondary || []
    }
  }
  return []
})
// 运营性常量（T2）：meta 下发 table → active 值；constant_ref 字段渲染下拉
//（retired 不在其中 ⇒ 新单据选不到；历史单据显示走 ext_json 的值快照）。
const constants = computed(() => (meta.value && meta.value.constants) || {})
const paymentOptions = computed(() => {
  const pm = meta.value && meta.value.enums && meta.value.enums.payment_method_input
  return (pm && pm.values) || []
})

// ---- 分档预览（debounce）----
function schedulePreview() {
  if (previewTimer) clearTimeout(previewTimer)
  previewTimer = setTimeout(runPreview, 400)
}
async function runPreview() {
  if (!curDocType.value || !meta.value) return
  const payload = {
    doc_type: curDocType.value,
    usage_category_l1: fields.usage_category_l1 || undefined,
    payment_method_input: fields.payment_method_input || undefined,
  }
  const cents = yuanToCents(fields.amount_cents)
  if (cents !== null) payload.amount_cents = cents
  previewHint.value = ''
  try {
    preview.value = await previewApproval(payload)
  } catch (e) {
    preview.value = null
    previewHint.value = e.message || String(e) // 400 的可见提示（如 PR<1000 引导走 BA）
  }
}

// ---- 字段渲染辅助 ----
function isMoney(field) { return field.type === 'money_cents' }
function yuanToCents(v) {
  if (v === undefined || v === null || String(v).trim() === '') return null
  const n = Number(v)
  if (!Number.isFinite(n)) return null
  return Math.round(n * 100)
}
function resetForm() {
  for (const k of Object.keys(fields)) delete fields[k]
  attachments.value = []
  preview.value = null
  previewHint.value = ''
  err.value = ''
  msg.value = ''
  idemKey.value = genIdemKey() // 换单据 ⇒ 新幂等键
}
function selectDoc(dt) {
  if (!dt || dt === curDocType.value) return
  curDocType.value = dt
  resetForm()
  router.replace({ name: 'submit', params: { docType: dt } })
  schedulePreview()
}

// ---- 附件 ----
async function onPickFile(e) {
  const file = e.target.files && e.target.files[0]
  if (!file) return
  err.value = ''
  uploading.value = true
  try {
    const up = await uploadApprovalAttachment(file)
    attachments.value = [...attachments.value, { file_id: up.file_id, file_name: up.file_name }]
  } catch (ex) {
    err.value = ex.message || String(ex)
  } finally {
    uploading.value = false
    e.target.value = ''
  }
}
function removeAttachment(fid) {
  attachments.value = attachments.value.filter((a) => a.file_id !== fid)
}

// ---- 提交 ----
function missingRequired() {
  const missing = []
  for (const f of visibleFields.value) {
    if (!f.required) continue
    if (isMoney(f)) {
      const cents = yuanToCents(fields[f.name])
      if (cents === null) missing.push(f.label)
      continue
    }
    const v = fields[f.name]
    if (v === undefined || v === null || String(v).trim() === '') missing.push(f.label)
  }
  return missing
}

async function doSubmit() {
  err.value = ''
  msg.value = ''
  if (!curForm.value) { err.value = '请先选择单据类型'; return }
  if (!approvalCode.value) {
    err.value = `单据类型 ${curDocType.value} 未配置 approval_code（请联系系统管理员执行配置映射导入）`
    return
  }
  const missing = missingRequired()
  if (missing.length) { err.value = `以下必填项未填：${missing.join('、')}`; return }

  // 组装 payload（服务端仍会完整校验 —— 前端校验只是即时提示）
  const payloadFields = {}
  for (const f of visibleFields.value) {
    const v = fields[f.name]
    if (v === undefined || v === null || v === '') continue
    payloadFields[f.name] = isMoney(f) ? yuanToCents(v) : v
  }
  const payload = {
    doc_type: curDocType.value,
    approval_code: approvalCode.value,
    usage_category_l1: fields.usage_category_l1 || undefined,
    usage_category_l2: fields.usage_category_l2 || undefined,
    payment_method_input: fields.payment_method_input || undefined,
    fields: payloadFields,
    attachment_ids: attachments.value.map((a) => a.file_id),
  }

  loading.value = true
  try {
    const data = await submitApproval(payload, idemKey.value)
    msg.value = data.idempotent_replay
      ? `幂等重放：复用首次单据 ${data.biz_no}`
      : `提交成功：${data.biz_no}`
    setTimeout(() => router.push(`/approval/${data.biz_no}`), 600)
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

function genIdemKey() {
  // 会话内幂等键：uuid v4（无依赖）
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0
    const v = c === 'x' ? r : (r & 0x3) | 0x8
    return v.toString(16)
  })
}

onMounted(async () => {
  err.value = ''
  try {
    meta.value = await fetchApprovalMeta()
    if (!curDocType.value || !meta.value.doc_types_available.includes(curDocType.value)) {
      curDocType.value = meta.value.doc_types_available[0] || ''
    }
    schedulePreview()
  } catch (e) {
    err.value = e.message || String(e)
  }
})

// 关键事实变化 ⇒ 重算预览
watch(() => [curDocType.value, fields.amount_cents, fields.usage_category_l1, fields.payment_method_input], schedulePreview)
</script>

<template>
  <div class="submit-page">
    <h2>发起申请</h2>
    <p v-if="meta" class="spec-ver">契约版本：{{ meta.spec_version }}</p>

    <div v-if="err" class="alert err">{{ err }}</div>
    <div v-if="msg" class="alert ok">{{ msg }}</div>

    <div v-if="!meta" class="loading">加载表单元数据…</div>

    <template v-if="meta">
      <!-- 单据类型选择 -->
      <div class="doc-picker">
        <button
          v-for="dt in meta.doc_types_available"
          :key="dt"
          class="doc-tab"
          :class="{ active: dt === curDocType }"
          @click="selectDoc(dt)"
        >
          {{ (forms.find((f) => f.doc_type === dt) || {}).label || dt }}
        </button>
      </div>

      <!-- 分档/链预览 -->
      <div v-if="preview || previewHint" class="preview-box">
        <div v-if="previewHint" class="alert err">{{ previewHint }}</div>
        <template v-if="preview">
          <div class="preview-line">
            <span class="tag">档位</span> {{ preview.tier }}
            <span class="tag">流程线</span> {{ preview.route.label }}
            <span v-if="preview.route.payment" class="tag">付款</span>
            <span v-if="preview.route.payment">{{ preview.route.payment }}</span>
          </div>
          <div class="preview-nodes">
            <template v-for="n in preview.nodes" :key="n.source_node_id + n.seq">
              <span
                class="node"
                :class="{ action: !n.is_approval, pending: n.is_approval && !n.resolved, cosign: n.co_sign_count >= 2 }"
                :title="n.branch_note || ''"
              >
                <template v-if="n.is_approval">
                  {{ n.seq }}. {{ n.node_name }}<template v-if="n.co_sign_count >= 2">
                    （{{ n.co_sign_count }} 人会签）</template><template v-else-if="n.approvers.length">
                    （{{ n.approvers.map((a) => a.name).join('、') }}）</template><template v-else>（缺人）</template>
                </template>
                <template v-else>· {{ n.node_name }}</template>
              </span>
            </template>
          </div>
          <!-- N-014：≥3 会签等非阻断告警 -->
          <div v-for="(w, i) in preview.warnings || []" :key="'w' + i" class="alert warn">{{ w }}</div>
          <div v-if="preview.unresolved_roles.length" class="alert err">
            以下环节算不到审批人，提交将被阻断：{{
              preview.unresolved_roles.map((u) => `${u.node_name}（${u.role}：${u.reason}）`).join('；')
            }}
          </div>
        </template>
      </div>

      <!-- 表单（meta 驱动） -->
      <form class="form" @submit.prevent="doSubmit">
        <template v-for="f in visibleFields" :key="f.name">
          <label class="row">
            <span class="lbl">
              {{ f.label }}<em v-if="f.required" class="req">*</em>
            </span>

            <!-- 金额（元输入 → 分提交） -->
            <input
              v-if="isMoney(f)"
              v-model="fields[f.name]"
              class="inp"
              type="number"
              min="0"
              step="0.01"
              placeholder="元（提交按分换算）"
            >

            <!-- 用途分类一级 -->
            <select
              v-else-if="f.name === 'usage_category_l1' && usageL1Options.length"
              v-model="fields[f.name]"
              class="inp"
            >
              <option value="">请选择</option>
              <option v-for="o in usageL1Options" :key="o.code" :value="o.code">{{ o.label }}</option>
            </select>

            <!-- 二级明细（随一级联动） -->
            <select
              v-else-if="f.name === 'usage_category_l2' && usageL2Options.length"
              v-model="fields[f.name]"
              class="inp"
            >
              <option value="">请选择</option>
              <option v-for="s in usageL2Options" :key="s" :value="s">{{ s }}</option>
            </select>

            <!-- 支付方式 -->
            <select
              v-else-if="f.name === 'payment_method_input' && paymentOptions.length"
              v-model="fields[f.name]"
              class="inp"
            >
              <option value="">请选择</option>
              <option v-for="p in paymentOptions" :key="p" :value="p">{{ p }}</option>
            </select>

            <!-- 运营性常量（constant_ref，T2）：从 meta.constants[字段名] 取 active 值 -->
            <select
              v-else-if="f.type === 'constant_ref' && constants[f.name]"
              v-model="fields[f.name]"
              class="inp"
            >
              <option value="">请选择</option>
              <option v-for="v in constants[f.name]" :key="v" :value="v">{{ v }}</option>
            </select>

            <!-- 枚举（自带 values） -->
            <select v-else-if="f.type === 'enum' && f.values && f.values.length" v-model="fields[f.name]" class="inp">
              <option value="">请选择</option>
              <option v-for="v in f.values" :key="v" :value="v">{{ v }}</option>
            </select>

            <input
              v-else-if="f.type === 'date'"
              v-model="fields[f.name]"
              class="inp"
              type="date"
            >
            <input
              v-else-if="f.type === 'number'"
              v-model="fields[f.name]"
              class="inp"
              type="number"
            >
            <!-- 布尔（N-013 is_fixed_asset 等）：★ 不预置初值 —— 必填布尔须显式勾选作答 -->
            <label v-else-if="f.type === 'boolean'" class="bool-lbl">
              <input
                v-model="fields[f.name]"
                type="checkbox"
                @change="fields[f.name] = $event.target.checked"
              >
              {{ f.rule || '是 / 否（须显式勾选）' }}
            </label>
            <input
              v-else
              v-model="fields[f.name]"
              class="inp"
              type="text"
              :placeholder="f.required_conditional ? `条件必填：${f.required_conditional}` : ''"
            >
          </label>
        </template>

        <!-- 附件（提交时点有附件字段时展示上传位；服务端按 schema 校验条件必填） -->
        <div class="row">
          <span class="lbl">附件</span>
          <div class="files">
            <span v-for="a in attachments" :key="a.file_id" class="file-chip">
              {{ a.file_name }}
              <a href="javascript:;" @click="removeAttachment(a.file_id)">移除</a>
            </span>
            <input type="file" :disabled="uploading" @change="onPickFile">
            <span v-if="uploading" class="hint">上传中…</span>
          </div>
        </div>

        <div class="actions">
          <button class="btn primary" type="submit" :disabled="loading">
            {{ loading ? '提交中…' : '提交审批' }}
          </button>
          <span v-if="preview && preview.unresolved_roles.length" class="hint warn">
            存在缺人环节，提交将被拒绝
          </span>
        </div>
      </form>
    </template>
  </div>
</template>

<style scoped>
.submit-page { padding: 8px 4px 40px; max-width: 860px; }
.spec-ver { color: #888; font-size: 12px; }
.doc-picker { display: flex; gap: 8px; margin: 12px 0; flex-wrap: wrap; }
.doc-tab { padding: 6px 14px; border: 1px solid #d0d7de; border-radius: 6px; background: #fff; cursor: pointer; }
.doc-tab.active { background: #0969da; border-color: #0969da; color: #fff; }
.preview-box { background: #f6f8fa; border: 1px solid #d0d7de; border-radius: 8px; padding: 10px 12px; margin-bottom: 14px; }
.preview-line { font-size: 14px; margin-bottom: 6px; }
.tag { display: inline-block; background: #ddf4ff; color: #0969da; border-radius: 4px; padding: 0 6px; margin: 0 6px 0 10px; font-size: 12px; }
.preview-nodes { display: flex; flex-wrap: wrap; gap: 6px; }
.node { font-size: 12px; background: #fff; border: 1px solid #d0d7de; border-radius: 12px; padding: 2px 8px; }
.node.action { color: #666; border-style: dashed; }
.node.pending { border-color: #cf222e; color: #cf222e; }
.node.cosign { border-color: #bf8700; background: #fff8c5; }
.bool-lbl { flex: 1; display: flex; gap: 8px; align-items: center; font-size: 14px; color: #57606a; }
.form .row { display: flex; align-items: center; gap: 10px; margin: 10px 0; }
.lbl { width: 190px; color: #24292f; font-size: 14px; text-align: right; flex-shrink: 0; }
.req { color: #cf222e; margin-left: 2px; font-style: normal; }
.inp { flex: 1; padding: 6px 10px; border: 1px solid #d0d7de; border-radius: 6px; font-size: 14px; }
.files { flex: 1; display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.file-chip { background: #ddf4ff; border-radius: 6px; padding: 2px 8px; font-size: 13px; }
.file-chip a { margin-left: 6px; color: #cf222e; text-decoration: none; }
.actions { margin-top: 16px; display: flex; gap: 12px; align-items: center; }
.btn { padding: 8px 18px; border-radius: 6px; border: 1px solid #d0d7de; background: #f6f8fa; cursor: pointer; font-size: 14px; }
.btn.primary { background: #2da44e; border-color: #2da44e; color: #fff; }
.btn:disabled { opacity: 0.6; cursor: not-allowed; }
.alert { border-radius: 6px; padding: 8px 12px; margin: 8px 0; font-size: 14px; }
.alert.err { background: #ffebe9; border: 1px solid #ff818266; color: #a40e26; }
.alert.ok { background: #dafbe1; border: 1px solid #4ac26b66; color: #116329; }
.alert.warn { background: #fff8c5; border: 1px solid #d4a72c66; color: #7d4e00; }
.hint { color: #888; font-size: 12px; }
.hint.warn { color: #cf222e; }
.loading { color: #888; padding: 20px 0; }
</style>
