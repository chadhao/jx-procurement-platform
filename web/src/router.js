// 路由表：登录页 / 看板 / 实例 / 台账 / 审计。
import { createRouter, createWebHistory } from 'vue-router'

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
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

export default router
