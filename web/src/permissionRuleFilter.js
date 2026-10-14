// permissionRuleFilter.js —— 权限矩阵「规则明细」分级筛选（纯函数，零依赖、node 直测）。
//
// ★ 动因（用户需求）：规则明细＝**角色数 × 资源数**（当前 9 × 6 ＝ 54 行）太长太复杂 ⇒ 加分级筛选。
// ★ 本模块**只影响显示**：不碰数据、不碰保存。`Admin.vue#saveRules` 仍提交**全量** `rules`
//   （筛选态只是视图层，"存的是筛出来的几条"这类误解必须从实现上杜绝）。
// ★ 抽出纯函数（而非写死在组件 computed 里）＝ 让「分级联动」这条**最容易漏的边界**可被
//   `permissionRuleFilter.spec.mjs` 直跑断言（同 `sysRole.js` / `dateRange.js` 的做法）。

// 「全部」哨兵：资源/角色筛选未选中时的取值（资源 key 与角色名均非空串，故无歧义）。
export const ALL = ''

// resourceOptions —— 资源下拉选项：从 `rules` 里**实际出现的资源去重**得来。
// ★ 不硬编码资源名（资源集来自后端 GET /api/permission-rules 的下发，会随配置演进）——
//   本仓库一贯教训：「写死的清单＝会漂移的清单」。
export function resourceOptions(rules) {
  const list = []
  const seen = new Set()
  for (const r of rules || []) {
    if (seen.has(r.resource)) continue
    seen.add(r.resource)
    list.push(r.resource)
  }
  return list
}

// roleOptionsFor —— 角色下拉选项：**分级联动**。
// ★ resource 为空（「全部资源」）⇒ 返回**全集**；选了某资源 ⇒ **只列该资源下实际存在的角色**。
export function roleOptionsFor(rules, resource) {
  const list = []
  const seen = new Set()
  for (const r of rules || []) {
    // ★★ 分级联动的那一「行」：资源不匹配的行直接跳过 ⇒ 角色选项随资源收窄。
    if (resource && r.resource !== resource) continue
    if (seen.has(r.role)) continue
    seen.add(r.role)
    list.push(r.role)
  }
  return list
}

// normalizeFilter —— 归一化筛选态：★ 若当前已选角色在（当前资源下的）新角色集里**不存在**
//   ⇒ **自动重置为「全部角色」**（不留一个查不出结果的筛选态）。资源改回「全部」时角色集恢复全集。
export function normalizeFilter(rules, filter) {
  const f = filter || {}
  const opts = roleOptionsFor(rules, f.resource)
  const role = f.role && !opts.includes(f.role) ? ALL : f.role || ALL
  return { resource: f.resource || ALL, role, keyword: f.keyword || '' }
}

// filterRules —— 按筛选态过滤明细行：资源精确匹配 · 角色精确匹配 · 关键词模糊匹配。
// ★ 关键词匹配 `resource` / `role` / 可写字段（`_writable`），★ 大小写不敏感。
export function filterRules(rules, filter) {
  const f = filter || {}
  const kw = String(f.keyword || '').trim().toLowerCase()
  return (rules || []).filter((r) => {
    if (f.resource && r.resource !== f.resource) return false
    if (f.role && r.role !== f.role) return false
    if (kw) {
      const hay = `${r.resource} ${r.role} ${r._writable || ''}`.toLowerCase()
      if (!hay.includes(kw)) return false
    }
    return true
  })
}
