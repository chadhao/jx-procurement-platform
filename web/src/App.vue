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
const navItems = computed(() => {
  const items = [
    { to: '/dashboard', label: '看板' },
    { to: '/instances', label: '审批实例' },
    { to: '/ledger', label: '台账' },
    { to: '/audit', label: '审计日志' },
  ]
  if (session.me && session.me.role === '系统管理员') {
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
      <div class="brand">江熙 · 采购与费用审批</div>
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
