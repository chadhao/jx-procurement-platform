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

/** 操作留痕 op_type → 中文（t_flow_op_log；四操作 + 回调）。 */
export function opTypeLabel(op) {
  const map = {
    SUBMIT: '提交',
    APPROVE: '同意',
    REJECT: '拒绝',
    TRANSFER: '转交',
    ADDSIGN: '加签',
    ROLLBACK: '回退',
    CANCEL: '撤回',
  }
  return map[(op || '').toUpperCase()] || op || '-'
}

export function pathOf(obj, key) {
  if (!obj) return undefined
  return obj[key]
}
