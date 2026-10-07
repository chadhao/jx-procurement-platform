// sysRole.js —— 系统角色可见性判据（N-076）。
// ★ App.vue 与 views/Admin.vue **同源**引用本模块，勿各写一遍（三处各写一遍正是 N-076 根因）。
// ★ 判据：审批角色残留值 `role === '系统管理员'` **或** `sys_roles` 含之 ——
//   保留 role 分支以兼容迁移前/直写脏数据（0022 已迁存量，但异常库仍可能出现该值）。
// ★ 数据源＝`/api/me` 的 `sys_roles`（恒数组，空 ⇒ []，见 handlers_biz#handleMe）。

export const SYS_ADMIN = '系统管理员'

// sysRolesOf 取系统角色列表（me 缺失 / 字段缺失 ⇒ []，前端免判 null）。
export function sysRolesOf(me) {
  return (me && me.sys_roles) || []
}

// isSysAdminOf 是否按系统管理员展示管理入口（菜单可见性提示；安全边界在服务端）。
export function isSysAdminOf(me) {
  const role = me && me.role
  return role === SYS_ADMIN || sysRolesOf(me).includes(SYS_ADMIN)
}
