// 展示层小工具。
export function statusClass(status) {
  switch ((status || '').toUpperCase()) {
    case 'APPROVED':
      return 'ok'
    case 'REJECTED':
      return 'danger'
    case 'PENDING':
      return 'info'
    case 'OVERTIME_CLOSE':
      return 'warn'
    default:
      return ''
  }
}

export function statusLabel(status) {
  const map = {
    PENDING: '审批中',
    APPROVED: '已通过',
    REJECTED: '已驳回',
    CANCELED: '已撤回',
    DELETED: '已删除',
    OVERTIME_CLOSE: '超时关闭',
  }
  return map[(status || '').toUpperCase()] || status || '-'
}

export function fmtTime(s) {
  if (!s) return '-'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleString('zh-CN', { hour12: false })
}

export function pathOf(obj, key) {
  if (!obj) return undefined
  return obj[key]
}
