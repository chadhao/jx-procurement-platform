// 路由表：登录页 / 看板 / 实例 / 台账 / 审计。
import { createRouter, createWebHistory } from 'vue-router'
import { fetchAuthorizeUrl } from './api'
import { refreshSession, session } from './store'

const routes = [
  { path: '/', redirect: '/dashboard' },
  { path: '/login', name: 'login', component: () => import('./views/Login.vue') },
  { path: '/dashboard', name: 'dashboard', component: () => import('./views/Dashboard.vue') },
  { path: '/instances', name: 'instances', component: () => import('./views/Instances.vue') },
  { path: '/instances/:code', name: 'instance-detail', component: () => import('./views/InstanceDetail.vue'), props: true },
  // 审批流转（M9 · 转向 ③ 新增）：我的待办 + 审批操作台。
  { path: '/tasks', name: 'my-tasks', component: () => import('./views/MyTasks.vue') },
  { path: '/approval/:bizNo', name: 'approval-console', component: () => import('./views/ApprovalConsole.vue'), props: true },
  { path: '/ledger', name: 'ledger', component: () => import('./views/Ledger.vue') },
  { path: '/petty-cash', name: 'petty-cash', component: () => import('./views/PettyCash.vue') },
  { path: '/submission', name: 'submission', component: () => import('./views/Submission.vue') },
  { path: '/reimbursement', name: 'reimbursement', component: () => import('./views/Reimbursement.vue') },
  { path: '/audit', name: 'audit', component: () => import('./views/Audit.vue') },
  { path: '/admin', name: 'admin', component: () => import('./views/Admin.vue') },
  // ★ /auth/feishu/callback 是**后端路由**（成功后 302 → /），前端不得占用；
  //   其余未匹配路径兜底到看板。
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// ---- 自动免登（飞书授权登录）----
// 公开页：无需会话。
const PUBLIC_PATHS = new Set(['/login'])
// 防重入标记：同一浏览器会话内自动跳授权页只尝试一次，避免「授权失败 → 回来 → 再跳」死循环。
const SSO_ATTEMPT_KEY = 'jx_sso_attempted'

router.beforeEach(async (to) => {
  if (PUBLIC_PATHS.has(to.path)) return true

  // 探测会话（只在首次进入时调一次 /api/me）。
  if (!session.ready) {
    try {
      await refreshSession()
    } catch (e) {
      // 网络/服务端异常：不自动跳授权页，落到登录页可见报错。
      session.ready = true
      return { path: '/login', query: { error: 'probe_failed' } }
    }
  }
  if (session.authenticated) {
    sessionStorage.removeItem(SSO_ATTEMPT_KEY)
    return true
  }

  // 防重入：已经自动尝试过一次仍未建立会话 ⇒ 不再跳转，落登录页。
  if (sessionStorage.getItem(SSO_ATTEMPT_KEY)) {
    return { path: '/login', query: { error: 'sso_loop' } }
  }

  // 未登录 ⇒ 自动发起飞书免登：拿授权 URL 后整页跳转（replace，避免历史栈回退到受保护页）。
  try {
    const data = await fetchAuthorizeUrl()
    if (!data || !data.authorize_url) throw new Error('authorize_url 为空')
    sessionStorage.setItem(SSO_ATTEMPT_KEY, '1')
    window.location.replace(data.authorize_url)
    return false // 等待整页跳转
  } catch (e) {
    // authorize-url 拿不到（如未配置 JX_APP_ID / 回调地址）⇒ 落登录页可见报错，不白屏。
    return { path: '/login', query: { error: 'authorize_unavailable' } }
  }
})

export default router
