<script setup>
// 系统管理（M5 / FR-M5-09~11）：① 权限矩阵（角色 × 资源）② 人员角色。
// 仅「系统管理员」可见；服务端仍二次校验（前端隐藏不构成安全边界，架构 §5.6）。
import { computed, onMounted, reactive, ref } from 'vue'
import {
  fetchPermissionRules,
  savePermissionRules,
  fetchAdminUsers,
  createAdminUser,
  patchAdminUser,
  fetchApprovalMeta,
  fetchConstants,
  createConstant,
  patchConstant,
  fetchRoleAgents,
  createRoleAgent,
  patchRoleAgent,
  fetchOrgUsers,
  syncOrgDirectory,
  fetchColumnCatalog,
} from '../api'
import ColumnListEditor from '../components/ColumnListEditor.vue'
import { session } from '../store'
import { isSysAdminOf } from '../sysRole'
import { fmtTime } from '../utils'
// ★ 规则明细分级筛选（纯函数，可 node 直测）：只影响**显示**，不改数据/不改保存。
import {
  ALL as FILTER_ALL,
  resourceOptions,
  roleOptionsFor,
  normalizeFilter,
  filterRules,
} from '../permissionRuleFilter'
// ★ 三列「可编辑列表」纯函数（chip 文案/去重增删/目录下拉/保存载荷，可 node 直测）。
import { buildSavePayload } from '../permissionColumnList'

const activeTab = ref('matrix')
const loading = ref(false)
const err = ref('')
const msg = ref('')

const resources = ref([])
const roles = ref([])
const rowScopes = ref([])
const rules = ref([])
// ★ 字段值域目录（GET /api/admin/permission-column-catalog）：三列可编辑列表的合法值＋中文标签来源。
//   ★ 数据源＝后端运行时 specload.Forms ＋ 既有规则取值（不新增 spec 文件 ⇒ 无第二份真相）。
//   ★ 加载失败**可见不阻断**：catalogErr 显示在含义说明块，编辑与保存不受影响（目录是辅助不是约束）。
const columnCatalog = ref([])
const kindsLegend = ref([])
const catalogErr = ref('')

// ★ N-076：与 App.vue 同源判据（sysRole.js）—— role 残留值 OR sys_roles 含系统管理员；
//   仅 sys_roles 路径下 role 为空也判真（郝端双身份 / 仅系统角色者均可见管理页）。
const isSysAdmin = computed(() => isSysAdminOf(session.me))

function splitList(text) {
  return String(text || '')
    .split(/[,，\n]/)
    .map((s) => s.trim())
    .filter(Boolean)
}
function joinList(arr) {
  return Array.isArray(arr) ? arr.join(', ') : ''
}

function ruleKey(resource, role) {
  return `${resource}||${role}`
}
const ruleMap = computed(() => {
  const m = {}
  for (const r of rules.value) m[ruleKey(r.resource, r.role)] = r
  return m
})
function findRule(resource, role) {
  return ruleMap.value[ruleKey(resource, role)]
}

// ---- 规则明细 · 分级筛选（★ 只影响显示，不改数据、不改保存逻辑）----
// ★ 保存仍提交**全量** `rules`（见 saveRules）—— 筛选态只是视图层。
const ruleFilter = reactive({ resource: FILTER_ALL, role: FILTER_ALL, keyword: '' })
// ★ 选项从 `rules` 里**实际出现者去重**得来，不硬编码资源名/角色名（会随配置演进）。
const filterResourceOptions = computed(() => resourceOptions(rules.value))
// ★ ★ 分级联动：角色选项随已选资源**收窄**（资源＝「全部」时恢复全集）。
const filterRoleOptions = computed(() => roleOptionsFor(rules.value, ruleFilter.resource))
const filteredRules = computed(() => filterRules(rules.value, ruleFilter))
// ★ 资源变更：若已选角色在新资源下不存在 ⇒ 归一化会**自动重置为「全部角色」**（不留查不出结果的筛选态）。
function onFilterResourceChange(v) {
  Object.assign(ruleFilter, normalizeFilter(rules.value, { ...ruleFilter, resource: v }))
}
function resetRuleFilter() {
  Object.assign(ruleFilter, normalizeFilter(rules.value, { resource: FILTER_ALL, role: FILTER_ALL, keyword: '' }))
}

async function loadRules() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchPermissionRules()
    resources.value = data.resources || []
    roles.value = data.roles || []
    rowScopes.value = data.row_scopes || []
    // ★ 三列改「可编辑列表」后，行内模型由逗号串改为**数组**（保存载荷形状不变，见 saveRules）。
    //   ★ 两套命名：column_allow/deny＝英文键；writable_fields＝中文列名（数组直存，chip 展示时给中文）。
    rules.value = (data.items || []).map((r) => ({
      resource: r.resource,
      role: r.role,
      row_scope: r.row_scope,
      _allow: [...(r.column_allow || [])],
      _deny: [...(r.column_deny || [])],
      _writable: [...(r.writable_fields || [])],
    }))
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

// 字段值域目录（填表辅助；失败可见但不阻断编辑/保存）。
async function loadColumnCatalog() {
  catalogErr.value = ''
  try {
    const data = await fetchColumnCatalog()
    columnCatalog.value = data.items || []
    kindsLegend.value = data.kinds_legend || []
  } catch (e) {
    columnCatalog.value = []
    kindsLegend.value = []
    catalogErr.value = e.message || String(e)
  }
}

async function saveRules() {
  err.value = ''
  msg.value = ''
  // ★ 保存契约未变：仍是 { rules: [{resource, role, row_scope, column_allow[], column_deny[], writable_fields[]}] }
  //   —— 可编辑列表只改编辑形态（数组直出，不再 join+split 往返；等价性见 permissionColumnList.spec.mjs）。
  const payload = buildSavePayload(rules.value)
  loading.value = true
  try {
    const data = await savePermissionRules(payload)
    msg.value = `已保存 ${data.saved} 条规则，生效方式：${data.effective}（无需重启）`
    await loadRules()
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

// ---- 人员角色 ----
const users = ref([])
// ★ N-042 / FR-M5-10：镜像选人数据源（GET /api/org/users —— **不得手填 open_id**）。
const orgUsers = ref([])
async function loadOrgUsers() {
  try {
    const data = await fetchOrgUsers()
    orgUsers.value = (data && data.items) || []
  } catch (e) {
    err.value = e.message || String(e)
  }
}
// ★ 姓名 / 主部门**不在本页维护**（飞书权威）——新增表单只需"点选镜像内的人 + 选角色"。
const newUser = reactive({ open_id: '', role: '', extra_depts: '', active: true })

async function loadUsers() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchAdminUsers()
    users.value = (data.items || []).map((u) => ({
      ...u,
      _extra: joinList(u.extra_depts),
      // ★ 未绑角色者后端返回 active=null（无启用态）。此处默认勾选，使「选角色 + 保存」
      //   后新人**立即启用**——否则会写出 active=false 的行，正是本 bug 的形态。
      active: u.active === null || u.active === undefined ? true : u.active,
    }))
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

async function addUser() {
  err.value = ''
  msg.value = ''
  try {
    // ★ 不提交 name / department：后端按 open_id 从飞书镜像取值（值域＝镜像在册者）。
    await createAdminUser({
      open_id: newUser.open_id,
      role: newUser.role,
      extra_depts: splitList(newUser.extra_depts),
      active: newUser.active,
    })
    msg.value = `已新增人员角色：${newUser.open_id}`
    Object.assign(newUser, { open_id: '', role: '', extra_depts: '', active: true })
    await loadUsers()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function saveUser(u) {
  err.value = ''
  msg.value = ''
  try {
    // ★ 只提交可改字段：角色 / 分管部门 / 启用（姓名、主部门来自飞书，后端拒绝其变更）。
    await patchAdminUser(u.open_id, {
      role: u.role,
      extra_depts: splitList(u._extra),
      active: u.active,
    })
    msg.value = `已保存：${u.open_id}（下一次请求即时生效）`
    await loadUsers()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

// ---- 手动同步飞书通讯录（★ 用户需求：「人员管理」页「手动同步飞书人员和部门」按钮）----
// 现状：全量同步能力早已具备，但既有入口 POST /internal/org/sync 属内部凭据域、前端调不到
// ⇒ 新增管理员端入口 POST /api/admin/org/sync（走 requireSysAdmin），本按钮即其前端入口。
const syncing = ref(false)
const syncResult = ref(null)

async function syncDirectory() {
  err.value = ''
  msg.value = ''
  syncResult.value = null
  syncing.value = true // ★ 请求期间置 loading/禁用按钮，防重复点击
  try {
    const data = await syncOrgDirectory()
    syncResult.value = data
    msg.value =
      `飞书通讯录同步完成：部门 ${data.dept_total}` +
      `（新增 ${data.dept_added} / 更新 ${data.dept_updated} / 停用 ${data.dept_soft_deleted}），` +
      `人员 ${data.user_total}` +
      `（新增 ${data.user_added} / 更新 ${data.user_updated} / 停用 ${data.user_soft_deleted}）`
    // ★ 同步完刷新人员列表与镜像选人源 —— 否则页面仍是旧数据（本功能的目的：让新增人员立刻可用）。
    await Promise.all([loadUsers(), loadOrgUsers()])
  } catch (e) {
    // ★ 失败可见：不吞错误。
    err.value = e.message || String(e)
  } finally {
    syncing.value = false
  }
}

// ---- ③ 运营性常量（T2 · R-24：只停用不删；role_display_name 禁新增归 chain.json#roles）----
// ★ 表清单不写死 —— 取自 meta#constants 的 keys（spec/constants.json 是唯一真相）。
const constTables = ref([]) // [{key, activeValues}]
const constTable = ref('')
const constItems = ref([])
const constTableMeta = ref(null) // {label, delete_policy, used_by}
const newConst = reactive({ value: '', sort_order: 0 })

async function loadConstTables() {
  const data = await fetchApprovalMeta()
  const c = (data && data.constants) || {}
  constTables.value = Object.keys(c).map((k) => ({ key: k, activeValues: c[k] }))
  if (!constTable.value && constTables.value.length) constTable.value = constTables.value[0].key
}

async function loadConstants() {
  if (!constTable.value) return
  err.value = ''
  try {
    const data = await fetchConstants(constTable.value)
    constTableMeta.value = data.table || null
    constItems.value = data.items || []
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function addConstant() {
  err.value = ''
  msg.value = ''
  try {
    await createConstant({
      table: constTable.value,
      value: newConst.value,
      sort_order: Number(newConst.sort_order) || 0,
    })
    msg.value = `已新增：${constTable.value} / ${newConst.value}`
    newConst.value = ''
    await loadConstants()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function saveConstant(row) {
  err.value = ''
  msg.value = ''
  try {
    await patchConstant(row.id, { value: row.value, sort_order: row.sort_order, status: row.status })
    msg.value = `已保存：${row.value}（${row.status}）`
    await loadConstants()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function switchConstTable(k) {
  constTable.value = k
  await loadConstants()
}

// ---- ④ 角色代理人（N-028 · 授权配置；feature_enabled=true —— N-072：M9 落地、告示撤下「未启用」）----
const agents = ref([])
const agentFeature = ref(false)
const eligibleRoles = ref([])
const newAgent = reactive({ role_key: '', agent_open_id: '', note: '' })

async function loadAgents() {
  err.value = ''
  try {
    const data = await fetchRoleAgents()
    agents.value = data.items || []
    eligibleRoles.value = data.eligible_roles || []
    agentFeature.value = !!data.feature_enabled
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function addAgent() {
  err.value = ''
  msg.value = ''
  try {
    await createRoleAgent({
      role_key: newAgent.role_key,
      agent_open_id: newAgent.agent_open_id,
      note: newAgent.note,
    })
    msg.value = `已登记代理人：${newAgent.role_key} → ${newAgent.agent_open_id}`
    Object.assign(newAgent, { role_key: '', agent_open_id: '', note: '' })
    await loadAgents()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function saveAgent(row) {
  err.value = ''
  msg.value = ''
  try {
    await patchRoleAgent(row.id, { note: row.note, state: row.state })
    msg.value = `已保存：${row.role_key}（${row.state}）`
    await loadAgents()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function loadAll() {
  await Promise.all([loadRules(), loadUsers(), loadConstTables(), loadAgents(), loadOrgUsers(), loadColumnCatalog()])
  await loadConstants()
}

// 切页签时按需刷新（数据小，直接重拉）
async function goTab(tab) {
  activeTab.value = tab
  if (tab === 'constants') await loadConstants()
  if (tab === 'agents') await loadAgents()
}

onMounted(loadAll)
</script>

<template>
  <div>
    <div v-if="!isSysAdmin" class="panel">
      <div class="error">无权限：本页仅「系统管理员」可访问（服务端二次校验）。</div>
    </div>

    <template v-else>
      <div class="panel">
        <div class="toolbar">
          <button :class="activeTab === 'matrix' ? 'primary' : 'ghost'" @click="activeTab = 'matrix'">
            权限矩阵
          </button>
          <button :class="activeTab === 'users' ? 'primary' : 'ghost'" @click="activeTab = 'users'">
            人员角色
          </button>
          <button :class="activeTab === 'constants' ? 'primary' : 'ghost'" @click="goTab('constants')">
            常量管理
          </button>
          <button :class="activeTab === 'agents' ? 'primary' : 'ghost'" @click="goTab('agents')">
            角色代理人
          </button>
          <span class="muted">改数据即生效，无需重启（缓存自动失效）</span>
        </div>
        <div v-if="err" class="error">{{ err }}</div>
        <div v-if="msg" class="tag ok">{{ msg }}</div>
      </div>

      <!-- ① 权限矩阵 -->
      <div v-if="activeTab === 'matrix'" class="panel">
        <h2>角色 × 资源 · 行范围</h2>
        <div v-if="loading" class="empty">加载中…</div>
        <div v-else class="matrix-wrap">
          <table>
            <thead>
              <tr>
                <th>资源</th>
                <th v-for="r in roles" :key="r">{{ r }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="res in resources" :key="res.key">
                <td>{{ res.label }}</td>
                <td v-for="r in roles" :key="r">
                  <select v-if="findRule(res.key, r)" v-model="findRule(res.key, r).row_scope">
                    <option v-for="s in rowScopes" :key="s.token" :value="s.token">{{ s.token }}</option>
                  </select>
                  <span v-else class="muted">未配置</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="muted">
          行范围令牌：<span v-for="s in rowScopes" :key="s.token">{{ s.token }}＝{{ s.label }}；</span>
        </p>

        <h2>规则明细 · 列白/黑名单与可写字段（可编辑列表）</h2>
        <!-- ★ 含义说明块（用户原话：「我都不清楚黑名单、白名单和可写字段允许的值分别都有什么，
             且每个是什么含义，很难填这些空」）—— 默认收起（<details> 无 open），不撑爆页面；
             ★ row_scope 取值逐条来自 GET /api/admin/permission-rules 的 row_scopes（不硬编码）。 -->
        <details class="meaning">
          <summary>字段含义说明：行范围 / 列白名单 / 列黑名单 / 可写字段（点开展开）</summary>
          <div class="meaning-body">
            <p>
              <strong>行范围（row_scope）</strong>：决定该角色<strong>能看到哪些行</strong>（先行过滤，后配列）。
              取值（来自权限矩阵接口的 <code>row_scopes</code>，共 {{ rowScopes.length }} 个）：
            </p>
            <ul>
              <li v-for="s in rowScopes" :key="s.token"><code>{{ s.token }}</code> ＝ {{ s.label }}</li>
            </ul>
            <p>
              <strong>列白名单（column_allow）</strong>：<strong>只允许看</strong>这些列，其余全部隐藏；
              <strong>留空＝不限制</strong>（除黑名单外全部允许）。取值＝<strong>英文键</strong>。
            </p>
            <p>
              <strong>列黑名单（column_deny）</strong>：<strong>禁止看</strong>这些列；
              <strong>优先级高于白名单</strong>（同一列同时出现，以黑名单为准）。取值＝<strong>英文键</strong>。
              ★ 金额类键名（<code>amount_cents</code> / <code>amount_display</code> /
              <code>formula_flags</code> 等）一旦被禁，嵌套结构（如公式红标）里的金额也一并裁掉。
            </p>
            <p>
              <strong>可写字段（writable_fields）</strong>：允许在台账里<strong>直接修改</strong>的字段，其余只读。
              取值＝<strong>中文列名</strong>（如「经办状态」）。
            </p>
            <p class="meaning-warn">
              ★★ <strong>两套命名（务必注意）</strong>：列黑/白名单填<strong>英文键</strong>，可写字段填<strong>中文列名</strong>——
              填错不会报错、但完全不生效。下方三列都可从「字段目录」下拉选择：<strong>英文键与中文标签一一对应</strong>，
              chip 上中文在前、英文键在后，两处都能看到中文；也可手动输入（不在目录仍可按原值保存）。
            </p>
            <p v-if="catalogErr" class="error">
              字段目录加载失败：{{ catalogErr }}（不影响手动输入与保存；合法值提示暂不可用）
            </p>
            <template v-else>
              <p><strong>字段目录类别</strong>（下拉选项按此理解，共 {{ columnCatalog.length }} 个合法值）：</p>
              <ul>
                <li v-for="k in kindsLegend" :key="k.kind"><code>{{ k.kind }}</code> ＝ {{ k.meaning }}</li>
              </ul>
            </template>
          </div>
        </details>
        <!-- ★ 分级筛选：只影响显示；保存仍提交全量规则（下方「保存权限矩阵」） -->
        <div class="toolbar">
          <select :value="ruleFilter.resource" @change="onFilterResourceChange($event.target.value)">
            <option :value="FILTER_ALL">全部资源</option>
            <option v-for="res in filterResourceOptions" :key="res" :value="res">{{ res }}</option>
          </select>
          <select v-model="ruleFilter.role">
            <option :value="FILTER_ALL">全部角色</option>
            <option v-for="r in filterRoleOptions" :key="r" :value="r">{{ r }}</option>
          </select>
          <input v-model="ruleFilter.keyword" placeholder="关键词（资源 / 角色 / 可写字段）" />
          <button class="ghost" @click="resetRuleFilter">清空筛选</button>
          <!-- ★ 计数常显（N==M 也不隐藏），让用户知道是否被筛过 -->
          <span class="muted">显示 {{ filteredRules.length }} / 共 {{ rules.length }} 条</span>
        </div>
        <div v-if="filteredRules.length" class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>资源</th>
                <th>角色</th>
                <th>行范围</th>
                <th>列白名单（英文键）</th>
                <th>列黑名单（英文键）</th>
                <th>可写字段（中文列名）</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(r, idx) in filteredRules" :key="idx">
                <td>{{ r.resource }}</td>
                <td>{{ r.role }}</td>
                <td>{{ r.row_scope }}</td>
                <!-- ★ 三列可编辑列表：chip（中文 label 优先、英文 key 次级）＋ 目录下拉追加 ＋ 手动输入带合法值提示。
                     ★ 保存形状不变：v-model 出的数组原样进 PUT 载荷（见 saveRules / permissionColumnList.js）。 -->
                <td>
                  <ColumnListEditor v-model="r._allow" :catalog="columnCatalog" empty-hint="（空＝除黑名单外全部允许）" />
                </td>
                <td>
                  <ColumnListEditor v-model="r._deny" :catalog="columnCatalog" empty-hint="（空＝不额外禁止）" />
                </td>
                <td>
                  <ColumnListEditor v-model="r._writable" :catalog="columnCatalog" empty-hint="（空＝全部只读）" />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">当前筛选条件下没有规则</div>
        <div class="toolbar" style="margin-top: 12px">
          <button class="primary" :disabled="loading" @click="saveRules">保存权限矩阵</button>
        </div>
      </div>

      <!-- ② 人员角色 -->
      <div v-if="activeTab === 'users'" class="panel">
        <h2>人员角色（open_id ↔ 角色 / 分管部门；姓名 · 主部门来自飞书 · 只读）</h2>
        <!-- ★ 手动同步飞书通讯录（用户需求）：仅系统管理员可见本页，后端 requireSysAdmin 二次校验 -->
        <div class="toolbar">
          <button class="primary" :disabled="syncing" @click="syncDirectory">
            {{ syncing ? '同步中…' : '同步飞书通讯录' }}
          </button>
          <span class="muted">
            从飞书拉取最新的人员与部门（全量）；完成后自动刷新下方列表，新增人员立即可用
          </span>
        </div>
        <div v-if="syncResult" class="tag ok">
          同步结果：部门 {{ syncResult.dept_total }}（新增 {{ syncResult.dept_added }} /
          更新 {{ syncResult.dept_updated }} / 停用 {{ syncResult.dept_soft_deleted }}）；
          人员 {{ syncResult.user_total }}（新增 {{ syncResult.user_added }} /
          更新 {{ syncResult.user_updated }} / 停用 {{ syncResult.user_soft_deleted }}）；
          耗时 {{ syncResult.duration_ms }} ms
        </div>
        <div class="toolbar">
          <select v-model="newUser.open_id">
            <option value="" disabled>点选人员（镜像）</option>
            <option v-for="u in orgUsers" :key="u.open_id" :value="u.open_id">
              {{ u.name || u.open_id }}（{{ u.department || '—' }}）· {{ u.open_id }}
            </option>
          </select>
          <select v-model="newUser.role">
            <option value="">选择角色</option>
            <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
          </select>
          <input v-model="newUser.extra_depts" placeholder="分管部门(逗号)" />
          <label class="muted"><input type="checkbox" v-model="newUser.active" /> 启用</label>
          <button class="primary" @click="addUser">新增</button>
        </div>
        <p class="muted">
          列表＝<strong>飞书通讯录在册人员</strong>（未分配角色者也在列，可当场下拉分配）；
          <strong>姓名 / 主部门来自飞书，本页只读</strong> —— 如需修改请在飞书端改好后点「同步飞书通讯录」。
        </p>
        <div v-if="!users.length" class="empty">暂无人员（通讯录镜像为空，请先「同步飞书通讯录」）</div>
        <table v-else>
          <thead>
            <tr>
              <th>open_id</th>
              <th>姓名（来自飞书）</th>
              <th>角色</th>
              <th>主部门（来自飞书）</th>
              <th>分管部门</th>
              <th>启用</th>
              <th>更新时间</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.open_id">
              <td>{{ u.open_id }}</td>
              <td>{{ u.name || '—' }}</td>
              <td>
                <select v-model="u.role">
                  <option value="" disabled>未分配</option>
                  <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
                </select>
              </td>
              <td>{{ u.department || '—' }}</td>
              <td><input v-model="u._extra" placeholder="逗号分隔" /></td>
              <td><input type="checkbox" v-model="u.active" /></td>
              <td class="muted">{{ fmtTime(u.updated_at) }}</td>
              <td><button class="ghost" @click="saveUser(u)">保存</button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- ③ 运营性常量（T2 · R-24：只停用不删；role_display_name 禁新增 —— 归 chain.json#roles） -->
      <div v-if="activeTab === 'constants'" class="panel">
        <h2>运营性常量（改了不影响流程线的字典）</h2>
        <div class="toolbar">
          <select :value="constTable" @change="switchConstTable($event.target.value)">
            <option value="" disabled>选择常量表</option>
            <option v-for="t in constTables" :key="t.key" :value="t.key">{{ t.key }}</option>
          </select>
          <input
            v-if="constTable !== 'role_display_name'"
            v-model="newConst.value"
            placeholder="新值"
          />
          <input
            v-if="constTable !== 'role_display_name'"
            v-model.number="newConst.sort_order"
            type="number"
            placeholder="排序"
            style="width: 72px"
          />
          <button
            v-if="constTable !== 'role_display_name'"
            class="primary"
            :disabled="!newConst.value"
            @click="addConstant"
          >
            新增
          </button>
          <span v-if="constTable === 'role_display_name'" class="muted">
            角色显示名禁止新增（归 chain.json#roles；此处仅可改名 / 停用）
          </span>
        </div>
        <p v-if="constTableMeta" class="muted">
          {{ constTableMeta.label }} · 删除策略：{{ constTableMeta.delete_policy }} ·
          消费方：{{ constTableMeta.used_by }} —— 本页不提供删除按钮（后端 DELETE 恒 40900，只停用不删）
        </p>
        <div v-if="!constItems.length" class="empty">该表暂无条目</div>
        <table v-else>
          <thead>
            <tr>
              <th>值</th>
              <th>排序</th>
              <th>状态</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in constItems" :key="row.id">
              <td><input v-model="row.value" /></td>
              <td><input v-model.number="row.sort_order" type="number" style="width: 72px" /></td>
              <td>
                <select v-model="row.status">
                  <option value="active">active（生效）</option>
                  <option value="retired">retired（停用）</option>
                </select>
              </td>
              <td><button class="ghost" @click="saveConstant(row)">保存</button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- ④ 角色代理人（N-028 · 授权配置；N-072：feature_enabled=true ⇒ 正面说明块，告知边界 ≠ 堆说明） -->
      <div v-if="activeTab === 'agents'" class="panel">
        <h2>角色代理人（授权配置 —— 决定「谁能审」，区别于运营性常量）</h2>
        <div v-if="agentFeature" class="tag" style="background: #e8f7ee; border-color: #b7e1cd">
          ★ 代理人功能已启用：已登记的代理人在其被代理角色的本节点上可执行「转交 / 回退」；
          仅限本节点、不可加签、不可撤回；备付金两节点（approve_petty_cash / disburse）不接受代理人
          —— 完整口径见 spec/authority.json#enable_guard。
        </div>
        <div class="toolbar">
          <select v-model="newAgent.role_key">
            <option value="" disabled>选择角色</option>
            <option v-for="r in eligibleRoles" :key="r" :value="r">{{ r }}</option>
          </select>
          <select v-model="newAgent.agent_open_id">
            <option value="" disabled>点选代理人（镜像内用户，不手填 open_id）</option>
            <option v-for="u in users" :key="u.open_id" :value="u.open_id">
              {{ u.name || u.open_id }}（{{ u.open_id }}）
            </option>
          </select>
          <input v-model="newAgent.note" placeholder="备注（可选）" />
          <button
            class="primary"
            :disabled="!newAgent.role_key || !newAgent.agent_open_id"
            @click="addAgent"
          >
            登记
          </button>
        </div>
        <div v-if="!agents.length" class="empty">暂无代理人登记</div>
        <table v-else>
          <thead>
            <tr>
              <th>角色</th>
              <th>代理人</th>
              <th>备注</th>
              <th>状态</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in agents" :key="row.id">
              <td>{{ row.role_key }}</td>
              <td>{{ row.agent_open_id }}</td>
              <td><input v-model="row.note" placeholder="备注" /></td>
              <td>
                <select v-model="row.state">
                  <option value="active">active（生效）</option>
                  <option value="retired">retired（停用）</option>
                </select>
              </td>
              <td><button class="ghost" @click="saveAgent(row)">保存</button></td>
            </tr>
          </tbody>
        </table>
        <p class="muted">
          护栏（后端硬校验）：每角色至多 1 名生效代理人 · 相邻两级不得同一人代理 ·
          备付金两节点不适用 · 只停用不删（无删除按钮）·
          <strong>代理人不参与审批人解析</strong>（签批人不可用只阻断 40010，不替补 —— 用户定案）。
        </p>
      </div>
    </template>
  </div>
</template>
