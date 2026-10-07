// N-076 · sysRole 判据的机检断言（node 直跑，零依赖）。
// 运行：node web/src/sysRole.spec.mjs
// 判据①③④前端半边：郝端双身份 / 仅系统角色者 ⇒ isSysAdmin=true；
// 仅审批角色者 ⇒ false；残留 role=系统管理员 与 sys_roles 分支等价。
import assert from 'node:assert/strict'
import { isSysAdminOf, sysRolesOf, SYS_ADMIN } from './sysRole.js'

// ① 郝端场景：审批角色=项目总经理 + sys_roles 含系统管理员 ⇒ 管理菜单可见。
assert.equal(
  isSysAdminOf({ role: '项目总经理', sys_roles: [SYS_ADMIN] }),
  true,
  '双身份（sys_roles 含系统管理员）必须判真',
)

// ③ 仅系统角色者：role 空 + sys_roles 含系统管理员 ⇒ 判真。
assert.equal(
  isSysAdminOf({ role: '', sys_roles: [SYS_ADMIN] }),
  true,
  '仅系统角色者必须判真（菜单可见）',
)

// ② 仅审批角色者：验收人、无 sys_roles ⇒ 判假（菜单不显示）。
assert.equal(
  isSysAdminOf({ role: '验收人' }),
  false,
  '仅审批角色者必须判假',
)
assert.equal(
  isSysAdminOf({ role: '验收人', sys_roles: [] }),
  false,
  'sys_roles 空数组也必须判假',
)

// ④ 兼容：残留 role=系统管理员（sys_roles 空）⇒ 与 sys_roles 分支等价判真。
assert.equal(
  isSysAdminOf({ role: SYS_ADMIN, sys_roles: [] }),
  true,
  '残留 role 值必须与 sys_roles 分支等价（兼容分支保留）',
)

// 空形态免判空：me 缺失 / 字段缺失 ⇒ []（前端免判 null）。
assert.deepEqual(sysRolesOf(null), [], 'me=null ⇒ []')
assert.deepEqual(sysRolesOf({}), [], '缺 sys_roles 字段 ⇒ []')
assert.equal(isSysAdminOf(null), false, 'me=null ⇒ false')

console.log('sysRole.spec.mjs: all assertions passed')
