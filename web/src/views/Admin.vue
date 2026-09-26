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
} from '../api'
import { session } from '../store'
import { fmtTime } from '../utils'

const activeTab = ref('matrix')
const loading = ref(false)
const err = ref('')
const msg = ref('')

const resources = ref([])
const roles = ref([])
const rowScopes = ref([])
const rules = ref([])

const isSysAdmin = computed(() => (session.me && session.me.role === '系统管理员') || false)

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

async function loadAll() {
  await Promise.all([loadRules(), loadUsers()])
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
      <div v-else class="panel">
        <h2>人员角色（open_id ↔ 角色 / 部门 / 分管部门）</h2>
        <div class="toolbar">
          <input v-model="newUser.open_id" placeholder="open_id" />
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
    </template>
  </div>
</template>
