<script setup>
// 应用外壳：左侧导航 + 顶栏。登录态由 store 统一探测。
import { computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { session, refreshSession, clearSession } from './store'
import { logout } from './api'

const route = useRoute()
const router = useRouter()

onMounted(async () => {
  if (!session.ready) {
    try {
      await refreshSession()
    } catch (e) {
      /* 网络异常不阻塞外壳渲染 */
    }
  }
})

async function onLogout() {
  try {
    await logout()
  } catch (e) {
    /* 忽略注销接口异常，本地清理即可 */
  }
  clearSession()
  router.push('/login')
}

// 「系统管理」仅在角色为「系统管理员」时显示（前端隐藏不构成安全边界，服务端仍二次校验）。
// 备付金 / 报送菜单按角色显示（同样仅作导航提示，真实拦截在服务端）。
//
// ★ 菜单可见性必须与服务端 allow-list 逐条对齐，否则会出现「点得进去、取数 40300」的假入口：
//	备付金余额只读页：综合运营主管 + 主管领导（FR-M1-04；表 §3.6「可加：主管领导只读」）
//	报送：综合运营主管 + 项目总经理（§3.5）
//	★ 项目总经理是否应可见备付金余额——PRD 未列，暂按「不可见」实现并列入待确认，不擅自放宽。
const navItems = computed(() => {
  const items = [
    { to: '/dashboard', label: '看板' },
    { to: '/submit', label: '发起申请' },
    { to: '/tasks', label: '我的待办' },
    { to: '/instances', label: '审批实例' },
    { to: '/ledger', label: '台账' },
  ]
  const role = session.me && session.me.role
  if (role === '综合运营主管' || role === '主管领导') {
    items.push({ to: '/petty-cash', label: '备付金' })
  }
  if (role === '综合运营主管' || role === '项目总经理') {
    items.push({ to: '/submission', label: '报送' })
  }
  // 集团报销跟踪表（FR-M1-02）：写＝综合运营主管；读＝综合运营主管 / 主管领导 / 项目总经理 / 系统管理员。
  if (role === '综合运营主管' || role === '主管领导' || role === '项目总经理' || role === '系统管理员') {
    items.push({ to: '/reimbursement', label: '集团报销跟踪' })
  }
  items.push({ to: '/audit', label: '审计日志' })
  if (role === '系统管理员') {
    items.push({ to: '/admin', label: '系统管理' })
  }
  return items
})
</script>

<template>
  <div v-if="route.path === '/login'" class="login-shell">
    <router-view />
  </div>
  <div v-else class="layout">
    <aside class="sidebar">
      <div class="brand">江熙新材审批系统</div>
      <router-link
        v-for="item in navItems"
        :key="item.to"
        :to="item.to"
        class="nav-link"
        :class="{ active: route.path === item.to || route.path.startsWith(item.to + '/') }"
      >
        {{ item.label }}
      </router-link>
    </aside>
    <div class="main">
      <header class="topbar">
        <span class="who">
          <template v-if="session.me">
            {{ session.me.name || session.me.open_id }} · {{ session.me.role || '未映射角色' }}
          </template>
          <template v-else-if="session.ready">未登录</template>
          <template v-else>加载中…</template>
        </span>
        <button v-if="session.authenticated" class="ghost" @click="onLogout">退出</button>
      </header>
      <main class="content">
        <router-view />
      </main>
    </div>
  </div>
</template>
