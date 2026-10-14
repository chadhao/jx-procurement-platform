<script setup>
// ColumnListEditor.vue —— 权限矩阵三列（列白/黑名单、可写字段）的「可编辑列表」单元格。
//
// ★ 动因（用户原话）：「可写字段里边内容也太多了，做成一个可编辑列表。另外，我都不清楚
//   黑名单、白名单和可写字段允许的值分别都有什么，且每个是什么含义，很难填这些空。」
//   ⇒ 逗号分隔文本框 ⇒ chip 列表：已选项标签化（可删）＋ 字段目录下拉追加 ＋ 手动输入
//     （带合法值提示）。合法值与中文标签来源＝GET /api/admin/permission-column-catalog。
//
// ★★ 保存格式不变：本组件只改**编辑形态**，v-model 出的仍是 string[]，父级原样提交
//   PUT /api/admin/permission-rules（契约零变化；等价性由 permissionColumnList.spec.mjs 钉住）。
// ★ 纯函数全部在 permissionColumnList.js（node 直测），本文件只做装配。
import { ref, computed } from 'vue'
import {
  catalogByKey,
  chipParts,
  addValues,
  removeValue,
  catalogOptions,
  optionText,
  manualStatus,
  parseManualInput,
  nextDatalistId,
} from '../permissionColumnList'

const props = defineProps({
  // 当前取值数组（v-model）。列黑/白名单＝英文键；可写字段＝中文列名（两套命名，chip 都给中文）。
  modelValue: { type: Array, default: () => [] },
  // 字段目录 items（GET /api/admin/permission-column-catalog）。
  catalog: { type: Array, default: () => [] },
  // 空列表占位说明（每列语义不同：白名单空＝不限制、黑名单空＝不额外禁止、可写空＝全部只读）。
  emptyHint: { type: String, default: '（空）' },
})

const emit = defineEmits(['update:modelValue'])

const byKey = computed(() => catalogByKey(props.catalog))
// 下拉选项：目录原序、排除已选（防重复添加）。
const options = computed(() => catalogOptions(props.catalog, props.modelValue))
// 手动输入实时提示：目录内 ✓ / 目录外 ⚠（⚠ 仍可保存——目录是辅助，不是新约束）。
const status = computed(() => manualStatus(text.value, byKey.value))

const text = ref('')
const picked = ref('')
// datalist 需唯一 id（一页三列 × 多行 ⇒ 模块级计数器，见 permissionColumnList.js#nextDatalistId）。
const listId = nextDatalistId()

function commit() {
  const values = parseManualInput(text.value)
  if (!values.length) return
  emit('update:modelValue', addValues(props.modelValue, values))
  text.value = ''
}

function pick() {
  const v = picked.value
  if (!v) return
  emit('update:modelValue', addValues(props.modelValue, [v]))
  picked.value = '' // 复位 ⇒ 同一目录值可连续追加
}

function remove(v) {
  emit('update:modelValue', removeValue(props.modelValue, v))
}

// chip 悬停提示：同名不同标签如实列出（后端 label_conflict，不静默挑一个）。
function chipTitle(v) {
  const p = chipParts(v, byKey.value)
  return p.conflict.length ? `同名不同标签：${p.conflict.join('；')}` : ''
}
</script>

<template>
  <div class="clist">
    <div class="clist-chips">
      <span
        v-for="v in modelValue"
        :key="v"
        class="clist-chip"
        :class="{ unknown: !byKey.get(v) }"
        :title="chipTitle(v)"
      >
        <span class="clist-label">{{ byKey.get(v) ? chipParts(v, byKey).label : v }}</span>
        <span v-if="byKey.get(v) && chipParts(v, byKey).label !== v" class="clist-key">{{ v }}</span>
        <button class="clist-x" type="button" title="删除该值" @click="remove(v)">×</button>
      </span>
      <span v-if="!modelValue.length" class="muted">{{ emptyHint }}</span>
    </div>
    <div class="clist-add">
      <select v-model="picked" class="clist-pick" @change="pick">
        <option value="">＋ 从字段目录选择…</option>
        <option v-for="it in options" :key="it.key" :value="it.key">{{ optionText(it) }}</option>
      </select>
    </div>
    <div class="clist-add">
      <input
        v-model="text"
        class="clist-input"
        :list="listId"
        placeholder="搜索 / 手动输入，回车添加"
        @keyup.enter="commit"
      />
      <datalist :id="listId">
        <option v-for="it in catalog" :key="it.key" :value="it.key" :label="it.label || it.key" />
      </datalist>
      <button class="ghost" type="button" :disabled="!status.values.length" @click="commit">添加</button>
    </div>
    <div v-if="text.trim()" class="clist-hint">
      <span v-if="status.known.length" class="tag ok">
        ✓ 目录内：{{ status.known.map((k) => byKey.get(k).label || k).join('、') }}
      </span>
      <span v-if="status.unknown.length" class="tag warn">
        ⚠ 不在目录中：{{ status.unknown.join('、') }}（仍可按原值保存）
      </span>
    </div>
    <div v-else class="clist-hint muted">
      目录共 {{ catalog.length }} 个合法值；也可手动输入（不在目录仍可保存）
    </div>
  </div>
</template>

<style scoped>
.clist {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 260px;
  white-space: normal; /* 覆盖 td 的 nowrap：chip 与提示需换行 */
}

.clist-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.clist-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 6px;
  border-radius: 10px;
  font-size: 12px;
  background: var(--primary-weak);
  color: var(--primary);
  border: 1px solid #b7d0ff;
  max-width: 100%;
}

/* 未登记值（不在目录）：黄色以区别，但仍可保存（目录是辅助不是约束）。 */
.clist-chip.unknown {
  background: #fff3e6;
  color: var(--warn);
  border-color: #ffd8a8;
}

.clist-chip .clist-key {
  color: var(--muted);
  font-size: 11px;
}

.clist-chip .clist-x {
  border: none;
  background: none;
  padding: 0 2px;
  cursor: pointer;
  color: inherit;
  font-size: 12px;
  line-height: 1;
}

.clist-add {
  display: flex;
  gap: 4px;
}

.clist-add .clist-pick,
.clist-add .clist-input {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  padding: 4px 8px;
}

.clist-hint {
  font-size: 12px;
  line-height: 1.6;
}
</style>
