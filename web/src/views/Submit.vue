<script setup>
// 发起申请（M7 · 批 1）：meta 驱动表单 + 分档/链预览 + 提交。
//
// ★ D1/N-008 定案：表单 schema **后端权威**（spec/forms/*.json → GET /api/approval/meta），
//   本页只做渲染与即时提示，不持任何业务口径（校验以服务端为准）。
// ★ D5：分档/审批链预览走 POST /api/approval/preview —— 与提交**共算同一入口**，
//   前端不本地算分档；unresolved_roles 非空 ⇒ 提交将被阻断，预览先行暴露（R-g 缓解）。
// ★ 提交携带 Idempotency-Key（d9）：本页会话内固定一把，重复点击/网络重试不重复建单。
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  fetchApprovalMeta, previewApproval, submitApproval, uploadApprovalAttachment,
  fetchInstancePrefill, fetchOrgUsers,
} from '../api'
// N-050：重复段纯函数（不依赖 Vue/DOM —— Node 直跑验收）
import { rowEditableFieldsOf, parseBulkRows, locateFormErrors } from '../repeatRows'
// N-054 ①：date_range（iso_interval 载荷契约）纯函数
import { parseIsoInterval, buildIsoInterval } from '../dateRange'

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
// ★ N-040 裁定③：repeating section **不拍平** —— 行字段承载在 fields.<section_id>
// 数组里（服务端行级必填），此处只收集非 repeating 的平铺字段。
const visibleFields = computed(() => {
  const f = curForm.value
  if (!f) return []
  const out = []
  for (const sec of f.sections || []) {
    if (sec.filled_at) continue // 审批/拨付/后置/周期 section 不在提交时点渲染
    if (sec.repeating) continue // 明细行组走 repeatingData（N-040 契约）
    for (const field of sec.fields || []) {
      if (field.source === 'user') out.push({ ...field, sectionLabel: sec.label })
    }
  }
  return out
})

// ---- N-040 裁定③ · 重复段最小可用明细录入 ----
// 契约：fields.<section_id> = 数组 of 行对象（键＝行内字段名）。
// 本轮＝最小可用（增删行 + 行内字段按 schema 渲染 + 提交组装数组）；
// 完整明细 UI（拖拽/复制行/行内校验提示等）单独排期 —— 见 N-040 回执。
const repeatingSections = computed(() => {
  const f = curForm.value
  if (!f) return []
  return (f.sections || []).filter((s) => s.repeating && !s.filled_at)
})
// sectionId -> 行对象数组（每行一个 reactive 对象）
const repeatingData = reactive({})

function initRepeatingData() {
  for (const k of Object.keys(repeatingData)) delete repeatingData[k]
  for (const sec of repeatingSections.value) {
    repeatingData[sec.id] = [{}]
  }
}

function rowEditableFields(sec) {
  // N-050：唯一实现移到纯模块 repeatRows.js（Node 可直跑验收）；此处仅委托。
  return rowEditableFieldsOf(sec)
}

// ---- N-050 · 行级错误定位（契约 docs/05-API §4.6 form_errors）----
// keys ＝ `${section_id}|${row_index}|${field_name}`（row_index 1 起算；0＝段级/顶层）。
const formErrorKeys = ref([])
const formErrorFirst = ref(null)

function clearFormErrors() {
  formErrorKeys.value = []
  formErrorFirst.value = null
}
function hasSectionError(secId) {
  return formErrorKeys.value.includes(`${secId}|0|`)
}
function hasRowError(secId, ri) {
  const p = `${secId}|${ri + 1}|`
  // 行内字段级（fn 非空）与行本身（fn 空，如「第 N 行不是对象」）都算该行错误。
  return formErrorKeys.value.some((k) => k.startsWith(p))
}
function hasFieldError(secId, ri, fname) {
  return formErrorKeys.value.includes(`${secId}|${ri + 1}|${fname}`)
}
function hasTopFieldError(fname) {
  return formErrorKeys.value.some((k) => k.endsWith(`|0|${fname}`))
}
function clearRowErrors(secId, ri) {
  // 用户编辑该行 ⇒ 清该行高亮（避免陈旧红框）；顶层字段编辑清其字段键。
  const p = `${secId}|${ri + 1}|`
  formErrorKeys.value = formErrorKeys.value.filter((k) => !k.startsWith(p))
}
function scrollToFirstError() {
  const f = formErrorFirst.value
  if (!f) return
  let el = null
  if (f.row_index > 0) {
    el = document.querySelector(`[data-fe-row="${f.section_id}|${f.row_index}"]`)
    if (!el && f.field_name) {
      el = document.querySelector(`[data-fe-field="${f.section_id}|${f.row_index}|${f.field_name}"]`)
    }
  } else if (f.field_name) {
    el = document.querySelector(`[data-fe-topfield="${f.field_name}"]`)
  } else if (f.section_id) {
    el = document.querySelector(`[data-fe-section="${f.section_id}"]`)
  }
  if (el && el.scrollIntoView) el.scrollIntoView({ block: 'center' })
}

// ---- N-050 T3① 复制行：复制该行全部可编辑字段值，插到其后；新行不继承错误高亮 ----
function copyRepeatingRow(secId, idx) {
  const rows = repeatingData[secId]
  if (!rows || !rows[idx]) return
  const sec = repeatingSections.value.find((s) => s.id === secId)
  if (!sec) return
  const copy = {}
  for (const f of rowEditableFieldsOf(sec)) {
    const v = rows[idx][f.name]
    if (v !== undefined) copy[f.name] = v
  }
  rows.splice(idx + 1, 0, copy)
  clearFormErrors() // 行号位移 ⇒ 既有定位键全部失效（陈旧红框比没有更糟）
}

// ---- N-050 T3② 批量粘贴（页内 textarea；解析在纯模块 parseBulkRows）----
const bulkOpen = reactive({})
const bulkTexts = reactive({})
const bulkErrors = reactive({})

function toggleBulk(secId) {
  bulkOpen[secId] = !bulkOpen[secId]
  bulkErrors[secId] = ''
}
function applyBulk(sec) {
  const { rows, error } = parseBulkRows(rowEditableFields(sec), bulkTexts[sec.id] || '')
  if (error) {
    bulkErrors[sec.id] = error
    return // 可见报错，不静默丢弃
  }
  if (!rows.length) {
    bulkErrors[sec.id] = '没有可解析的行（非空行 0 条）'
    return
  }
  if (!repeatingData[sec.id]) repeatingData[sec.id] = []
  for (const r of rows) repeatingData[sec.id].push(r) // 追加，不覆盖
  bulkTexts[sec.id] = ''
  bulkErrors[sec.id] = ''
  bulkOpen[sec.id] = false
  clearFormErrors() // 行号位移 ⇒ 旧定位键失效
}

// ---- N-050 T4 拖拽排序（原生 HTML5；仅影响展示与提交数组顺序，无新语义）----
const dragState = reactive({ secId: '', idx: -1, overIdx: -1 })

function onRowDragStart(secId, idx) {
  dragState.secId = secId
  dragState.idx = idx
  dragState.overIdx = -1
}
function onRowDragOver(secId, idx) {
  if (dragState.idx < 0 || dragState.secId !== secId) return
  dragState.overIdx = idx
}
function onRowDrop(secId, idx) {
  const rows = repeatingData[secId]
  const from = dragState.idx
  dragState.secId = ''
  dragState.idx = -1
  dragState.overIdx = -1
  if (!rows || from < 0 || from === idx || from >= rows.length) return
  const [moved] = rows.splice(from, 1)
  rows.splice(idx, 0, moved)
  clearFormErrors() // 行号位移 ⇒ 旧定位键失效
}
function onRowDragEnd() {
  dragState.secId = ''
  dragState.idx = -1
  dragState.overIdx = -1
}

function addRepeatingRow(secId) {
  if (!repeatingData[secId]) repeatingData[secId] = []
  repeatingData[secId].push({})
}

function removeRepeatingRow(secId, idx) {
  const rows = repeatingData[secId]
  if (rows) rows.splice(idx, 1)
  clearFormErrors() // 行号位移 ⇒ 既有定位键失效（陈旧红框）
}

// 组装行数组：跳过完全空的行（用户点了「加行」没填不提交）；money 行内字段 元→分。
function buildRepeatingPayload() {
  const out = {}
  for (const sec of repeatingSections.value) {
    const rows = (repeatingData[sec.id] || []).filter((r) =>
      rowEditableFields(sec).some((f) => {
        const v = r[f.name]
        return v !== undefined && v !== null && String(v).trim() !== ''
      })
    )
    if (!rows.length) continue // 无有效行 ⇒ 不放键（行级必填由服务端 400 可见失败）
    out[sec.id] = rows.map((r) => {
      const row = {}
      for (const f of rowEditableFields(sec)) {
        const v = r[f.name]
        if (v === undefined || v === null || v === '') continue
        row[f.name] = isMoney(f) ? yuanToCents(v) : v
      }
      return row
    })
  }
  return out
}

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
// ---- N-060 F5（FR-M9-11）：person 字段镜像选人（下拉只出在职 —— 接口已滤离职/停用）----
const personUsers = ref([])
async function loadPersonUsers() {
  try {
    const data = await fetchOrgUsers({})
    personUsers.value = Array.isArray(data) ? data : (data && data.items) || []
  } catch (e) {
    personUsers.value = [] // 拉取失败 ⇒ 下拉空（提交期服务端仍会镜像命中阻断，不静默放行）
  }
}

// ---- N-054 ① · date_range（iso_interval：YYYY-MM-DD/YYYY-MM-DD）----
// 两个日期输入草稿 ⇒ 组装写回 fields（实时 ⇒ missingRequired/doSubmit 收集照旧）；
// 空 ⇒ fields 不带（收集处跳过）；起 > 止 ⇒ 可见报错、不得提交；既有值非法 ⇒ 提示不清空。
const dateRangeDrafts = reactive({}) // name → { start, end }
const dateRangeErrors = reactive({}) // name → 人读错误（''＝无）

function dateRangeFields() {
  return visibleFields.value.filter((f) => f.type === 'date_range')
}
function syncDraftFromField(name) {
  const r = parseIsoInterval(fields[name] || '')
  dateRangeDrafts[name] = { start: r.start, end: r.end }
  dateRangeErrors[name] = r.error // 非法 ⇒ 提示（不清空 fields —— 服务端结构化校验按原值判）
}
function onDateRangePick(name, side, ev) {
  if (!dateRangeDrafts[name]) dateRangeDrafts[name] = { start: '', end: '' }
  dateRangeDrafts[name][side] = ev && ev.target ? ev.target.value : ''
  const d = dateRangeDrafts[name]
  const r = buildIsoInterval(d.start, d.end)
  if (r.error) {
    dateRangeErrors[name] = r.error
    return // 不得提交（doSubmit 前置检查同款）
  }
  dateRangeErrors[name] = ''
  fields[name] = r.value // 空串 ⇒ 收集时不带该字段
}
// 外部改 fields（prefill/回填/换单据）⇒ 同步草稿；用户改草稿写回 ⇒ parse 幂等回同值。
watch(
  () => dateRangeFields().map((f) => `${f.name}=${fields[f.name] || ''}`),
  () => {
    for (const f of dateRangeFields()) syncDraftFromField(f.name)
  },
  { immediate: true },
)

function resetForm() {
  for (const k of Object.keys(fields)) delete fields[k]
  initRepeatingData() // ★ N-040：换单据重建明细行组（repeatingData 按当前表单 schema 初始化）
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
  clearFormErrors() // 下一次提交 ⇒ 清除陈旧高亮
  // N-054 ①：date_range 草稿错误（起>止 / 只填一半 / 既有值形态非法）⇒ 可见报错、不得提交
  for (const n of Object.keys(dateRangeErrors)) {
    if (dateRangeErrors[n]) {
      err.value = dateRangeErrors[n]
      return
    }
  }
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
  // ★ N-040 裁定③：repeating 明细行组 → fields.<section_id> = 数组 of 行对象
  Object.assign(payloadFields, buildRepeatingPayload())
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
    // N-050：40000 + data.form_errors ⇒ 行级定位高亮（docs/05-API §4.6）。
    // ★ 取不到 form_errors（40010 / 网络失败 / 老响应）⇒ 回落照旧显示 message
    //   —— 不得因取不到定位就不显示错误。
    err.value = e.message || String(e)
    const fe = e.code === 40000 && e.data && Array.isArray(e.data.form_errors)
      ? e.data.form_errors : null
    if (fe && fe.length) {
      const loc = locateFormErrors(fe)
      formErrorKeys.value = loc.keys
      formErrorFirst.value = loc.first
      nextTick(() => scrollToFirstError())
    } else {
      clearFormErrors()
    }
  } finally {
    loading.value = false
  }
}

// 付款路径显示名（T3/R-26；值本身由 spec payment_route_rule 下发，此处仅中文标注）。
function paymentRouteLabel(route) {
  const map = {
    group_public_account: '公户（集团执行）',
    petty_cash: '备付金直接支出',
    personal_advance_reimburse: '个人垫付 + 报销',
  }
  return map[route] || route || '-'
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
    initRepeatingData() // 初始进入也要按表单 schema 建明细行组
    loadPersonUsers() // N-060 F5：镜像人员（person 字段下拉）
    schedulePreview()
  } catch (e) {
    err.value = e.message || String(e)
  }
})

// 关键事实变化 ⇒ 重算预览
// ---- B6 · CT usage_category 从关联 PR 带入（UI 债 C）----
// ★ spec：CT#usage_category_l1/l2 source=system、rule「从已批准的 PR 带入、可改」——
//   回填后用户仍可改（可改但须在审批意见中说明理由 —— 判据/审批侧管，不在前端锁死）。
// ★ 键集合与后端白名单**同 spec 源**：source=system ∧ 非 related 带入排除集
//   （biz_no/related_biz_no/purchaser/approval_record_ref 各有生命周期，不是 related 来的）。
// ★ related 非 PR 号 / 拉取失败 ⇒ 静默不回填（关联单存在性由提交期判据管，此处不越权报错）。
const prefillExclude = new Set(['biz_no', 'related_biz_no', 'purchaser', 'approval_record_ref'])

async function fillFromRelated(f) {
  if (!f || f.name !== 'related_biz_no') return
  if (!curForm.value) return
  const rel = String(fields.related_biz_no || '').trim()
  if (!/^PR-\d{4}-\d{4}$/.test(rel)) return
  const keys = visibleFields.value
    .filter((x) => x.source === 'system' && !prefillExclude.has(x.name) && x.name !== f.name)
    .map((x) => x.name)
  if (!keys.length) return
  try {
    const data = await fetchInstancePrefill(rel, keys)
    const pf = (data && data.prefill) || {}
    for (const k of Object.keys(pf)) {
      if (pf[k] !== undefined && pf[k] !== null) fields[k] = pf[k]
    }
  } catch {
    // 静默：单号不存在 / 无权限 / 白名单外 —— 不阻断起草（提交期判据兜底）
  }
}

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
            <span class="tag">付款路径</span> {{ paymentRouteLabel(preview.payment_route) }}
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
          <label class="row" :data-fe-topfield="f.name">
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

            <!-- date_range（N-054 ①：iso_interval —— 两个日期输入，提交组装
                 YYYY-MM-DD/YYYY-MM-DD；不引入新依赖） -->
            <span v-else-if="f.type === 'date_range'" class="date-range">
              <input
                class="inp"
                type="date"
                :value="(dateRangeDrafts[f.name] || {}).start || ''"
                @change="onDateRangePick(f.name, 'start', $event)"
              >
              <span class="dr-sep">至</span>
              <input
                class="inp"
                type="date"
                :value="(dateRangeDrafts[f.name] || {}).end || ''"
                @change="onDateRangePick(f.name, 'end', $event)"
              >
              <em v-if="dateRangeErrors[f.name]" class="dr-error">{{ dateRangeErrors[f.name] }}</em>
            </span>

            <!-- N-060 F5（FR-M9-11）：person 字段＝镜像选人下拉（只列在职；不可选离职/停用）
                 ★ 提交期服务端仍做「镜像命不中 ⇒ 400 阻断」双保险（person_fields.go） -->
            <select
              v-else-if="f.type === 'person'"
              v-model="fields[f.name]"
              :class="['inp', { 'field-error': hasTopFieldError(f.name) }]"
              :data-fe-topfield="f.name"
            >
              <option value="">请选择</option>
              <option v-for="u in personUsers" :key="u.open_id" :value="u.name">{{ u.name }}</option>
            </select>

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
              @change="fillFromRelated(f)"
            >
          </label>
        </template>

        <!-- ★ N-040 裁定③ · 重复段最小可用明细录入（repeating section → fields.<id> 数组）
             完整明细 UI（拖拽排序/复制行/行内即时校验）单独排期 —— 见 COLLAB N-040 回执 -->
        <div
          v-for="sec in repeatingSections"
          :key="sec.id"
          class="repeating-block"
          :class="{ 'section-error': hasSectionError(sec.id) }"
          :data-fe-section="sec.id"
        >
          <div class="row">
            <span class="lbl">
              {{ sec.label || sec.id }}（明细行）
              <em v-if="rowEditableFields(sec).some((f) => f.required)" class="req">*</em>
            </span>
            <span class="hint">行内必填字段缺行将被服务端 400 拒绝</span>
          </div>
          <div
            v-for="(row, ri) in repeatingData[sec.id] || []"
            :key="ri"
            class="detail-row"
            :class="{
              'row-error': hasRowError(sec.id, ri),
              'drag-over': dragState.secId === sec.id && dragState.overIdx === ri && dragState.idx !== ri,
            }"
            :data-fe-row="`${sec.id}|${ri + 1}`"
            @input="clearRowErrors(sec.id, ri)"
            @change="clearRowErrors(sec.id, ri)"
            @dragover.prevent="onRowDragOver(sec.id, ri)"
            @drop.prevent="onRowDrop(sec.id, ri)"
          >
            <!-- 拖拽把手（仅把手可拖，避免与 input 内文本选择冲突） -->
            <span
              class="drag-handle"
              draggable="true"
              title="拖拽排序"
              @dragstart="onRowDragStart(sec.id, ri)"
              @dragend="onRowDragEnd"
            >⠿</span>
            <template v-for="f in rowEditableFields(sec)" :key="f.name">
              <!-- 金额（元输入 → 分提交；行内与顶层同口径） -->
              <input
                v-if="isMoney(f)"
                v-model="row[f.name]"
                :class="['inp', { 'field-error': hasFieldError(sec.id, ri, f.name) }]"
                :data-fe-field="`${sec.id}|${ri + 1}|${f.name}`"
                type="number"
                min="0"
                step="0.01"
                :placeholder="`${f.label}（元）`"
              >
              <select
                v-else-if="f.type === 'constant_ref' && constants[f.name]"
                v-model="row[f.name]"
                :class="['inp', { 'field-error': hasFieldError(sec.id, ri, f.name) }]"
                :data-fe-field="`${sec.id}|${ri + 1}|${f.name}`"
              >
                <option value="">请选择</option>
                <option v-for="v in constants[f.name]" :key="v" :value="v">{{ v }}</option>
              </select>
              <select
                v-else-if="f.type === 'enum' && f.values && f.values.length"
                v-model="row[f.name]"
                :class="['inp', { 'field-error': hasFieldError(sec.id, ri, f.name) }]"
                :data-fe-field="`${sec.id}|${ri + 1}|${f.name}`"
              >
                <option value="">请选择</option>
                <option v-for="v in f.values" :key="v" :value="v">{{ v }}</option>
              </select>
              <input
                v-else-if="f.type === 'number'"
                v-model.number="row[f.name]"
                :class="['inp', { 'field-error': hasFieldError(sec.id, ri, f.name) }]"
                :data-fe-field="`${sec.id}|${ri + 1}|${f.name}`"
                type="number"
                :placeholder="f.label"
              >
              <input
                v-else-if="f.type === 'date'"
                v-model="row[f.name]"
                :class="['inp', { 'field-error': hasFieldError(sec.id, ri, f.name) }]"
                :data-fe-field="`${sec.id}|${ri + 1}|${f.name}`"
                type="date"
              >
              <input
                v-else
                v-model="row[f.name]"
                :class="['inp', { 'field-error': hasFieldError(sec.id, ri, f.name) }]"
                :data-fe-field="`${sec.id}|${ri + 1}|${f.name}`"
                type="text"
                :placeholder="f.label"
              >
            </template>
            <button type="button" class="ghost" @click="copyRepeatingRow(sec.id, ri)">
              复制行
            </button>
            <button type="button" class="ghost" @click="removeRepeatingRow(sec.id, ri)">
              删行
            </button>
          </div>
          <div class="row">
            <button type="button" class="ghost" @click="addRepeatingRow(sec.id)">
              ＋ 添加一行
            </button>
            <button type="button" class="ghost" @click="toggleBulk(sec.id)">
              批量粘贴
            </button>
          </div>
          <!-- 批量粘贴（页内 textarea；解析见 repeatRows.parseBulkRows：Tab 分列、无 Tab 退化逗号） -->
          <div v-if="bulkOpen[sec.id]" class="bulk-paste">
            <textarea
              v-model="bulkTexts[sec.id]"
              class="bulk-textarea"
              rows="4"
              placeholder="每行一条记录：列顺序＝字段顺序；列间用 Tab（无 Tab 时按英文逗号）分隔；不识别表头"
            ></textarea>
            <div v-if="bulkErrors[sec.id]" class="bulk-error">{{ bulkErrors[sec.id] }}</div>
            <div class="row">
              <button type="button" class="ghost" @click="applyBulk(sec)">追加到明细</button>
              <button type="button" class="ghost" @click="toggleBulk(sec.id)">收起</button>
            </div>
          </div>
        </div>

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

/* ---- N-050 · 行级错误定位 / 拖拽 / 批量粘贴（复用既有样式体系，不引 UI 库）---- */
.row-error { outline: 2px solid #cf222e; outline-offset: 1px; background: #fff1f0; border-radius: 6px; }
.field-error { border-color: #cf222e !important; background: #fff1f0; }
.section-error { border-left: 3px solid #cf222e; padding-left: 8px; }
.drag-handle { cursor: grab; color: #888; user-select: none; padding: 0 4px; align-self: center; }
.drag-handle:active { cursor: grabbing; }
.detail-row.drag-over { outline: 2px dashed #0969da; outline-offset: 1px; }
.bulk-paste { margin: 6px 0 10px; }
.bulk-textarea { width: 100%; box-sizing: border-box; font-family: ui-monospace, monospace; font-size: 13px; padding: 6px 8px; border: 1px solid #d0d7de; border-radius: 6px; }
.bulk-error { color: #cf222e; font-size: 13px; margin-top: 4px; }

/* ---- N-054 ① · date_range（iso_interval）---- */
.date-range { display: flex; align-items: center; gap: 6px; flex: 1; flex-wrap: wrap; }
.dr-sep { color: #57606a; font-size: 13px; }
.dr-error { color: #cf222e; font-size: 12px; font-style: normal; flex-basis: 100%; }
</style>
