// 路由表：登录页 / 看板 / 实例 / 台账 / 审计。
import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  { path: '/', redirect: '/dashboard' },
  { path: '/login', name: 'login', component: () => import('./views/Login.vue') },
  { path: '/dashboard', name: 'dashboard', component: () => import('./views/Dashboard.vue') },
  { path: '/instances', name: 'instances', component: () => import('./views/Instances.vue') },
  { path: '/instances/:code', name: 'instance-detail', component: () => import('./views/InstanceDetail.vue'), props: true },
  { path: '/ledger', name: 'ledger', component: () => import('./views/Ledger.vue') },
  { path: '/audit', name: 'audit', component: () => import('./views/Audit.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

export default router
