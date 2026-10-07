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
} from '../api'
import { session } from '../store'
import { isSysAdminOf } from '../sysRole'
import { fmtTime } from '../utils'

const activeTab = ref('matrix')
const loading = ref(false)
const err = ref('')
const msg = ref('')

const resources = ref([])
const roles = ref([])
const rowScopes = ref([])
const rules = ref([])

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

async function loadRules() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchPermissionRules()
    resources.value = data.resources || []
    roles.value = data.roles || []
    rowScopes.value = data.row_scopes || []
    rules.value = (data.items || []).map((r) => ({
      resource: r.resource,
      role: r.role,
      row_scope: r.row_scope,
      _allow: joinList(r.column_allow),
      _deny: joinList(r.column_deny),
      _writable: joinList(r.writable_fields),
    }))
  } catch (e) {
    err.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

async function saveRules() {
  err.value = ''
  msg.value = ''
  const payload = rules.value.map((r) => ({
    resource: r.resource,
    role: r.role,
    row_scope: r.row_scope,
    column_allow: splitList(r._allow),
    column_deny: splitList(r._deny),
    writable_fields: splitList(r._writable),
  }))
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
const newUser = reactive({ open_id: '', name: '', role: '', department: '', extra_depts: '', active: true })

async function loadUsers() {
  loading.value = true
  err.value = ''
  try {
    const data = await fetchAdminUsers()
    users.value = (data.items || []).map((u) => ({
      ...u,
      _extra: joinList(u.extra_depts),
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
    await createAdminUser({
      open_id: newUser.open_id,
      name: newUser.name,
      role: newUser.role,
      department: newUser.department,
      extra_depts: splitList(newUser.extra_depts),
      active: newUser.active,
    })
    msg.value = `已新增人员角色：${newUser.open_id}`
    Object.assign(newUser, { open_id: '', name: '', role: '', department: '', extra_depts: '', active: true })
    await loadUsers()
  } catch (e) {
    err.value = e.message || String(e)
  }
}

async function saveUser(u) {
  err.value = ''
  msg.value = ''
  try {
    await patchAdminUser(u.open_id, {
      name: u.name,
      role: u.role,
      department: u.department,
      extra_depts: splitList(u._extra),
      active: u.active,
    })
    msg.value = `已保存：${u.open_id}（下一次请求即时生效）`
    await loadUsers()
  } catch (e) {
    err.value = e.message || String(e)
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
  await Promise.all([loadRules(), loadUsers(), loadConstTables(), loadAgents(), loadOrgUsers()])
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

        <h2>规则明细 · 列白/黑名单与可写字段（逗号分隔）</h2>
        <table>
          <thead>
            <tr>
              <th>资源</th>
              <th>角色</th>
              <th>行范围</th>
              <th>列白名单</th>
              <th>列黑名单</th>
              <th>可写字段</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(r, idx) in rules" :key="idx">
              <td>{{ r.resource }}</td>
              <td>{{ r.role }}</td>
              <td>{{ r.row_scope }}</td>
              <td><input v-model="r._allow" placeholder="留空=全部允许" /></td>
              <td><input v-model="r._deny" placeholder="deny 优先于 allow" /></td>
              <td><input v-model="r._writable" placeholder="仅运营表字段" /></td>
            </tr>
          </tbody>
        </table>
        <div class="toolbar" style="margin-top: 12px">
          <button class="primary" :disabled="loading" @click="saveRules">保存权限矩阵</button>
        </div>
      </div>

      <!-- ② 人员角色 -->
      <div v-if="activeTab === 'users'" class="panel">
        <h2>人员角色（open_id ↔ 角色 / 部门 / 分管部门）</h2>
        <div class="toolbar">
          <select v-model="newUser.open_id">
            <option value="" disabled>点选人员（镜像）</option>
            <option v-for="u in orgUsers" :key="u.open_id" :value="u.open_id">
              {{ u.name || u.open_id }}（{{ u.department || '—' }}）· {{ u.open_id }}
            </option>
          </select>
          <input v-model="newUser.name" placeholder="姓名" />
          <select v-model="newUser.role">
            <option value="">选择角色</option>
            <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
          </select>
          <input v-model="newUser.department" placeholder="主部门" />
          <input v-model="newUser.extra_depts" placeholder="分管部门(逗号)" />
          <label class="muted"><input type="checkbox" v-model="newUser.active" /> 启用</label>
          <button class="primary" @click="addUser">新增</button>
        </div>
        <div v-if="!users.length" class="empty">暂无人员映射（未映射人员默认拒绝）</div>
        <table v-else>
          <thead>
            <tr>
              <th>open_id</th>
              <th>姓名</th>
              <th>角色</th>
              <th>主部门</th>
              <th>分管部门</th>
              <th>启用</th>
              <th>更新时间</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.open_id">
              <td>{{ u.open_id }}</td>
              <td><input v-model="u.name" /></td>
              <td>
                <select v-model="u.role">
                  <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
                </select>
              </td>
              <td><input v-model="u.department" /></td>
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
