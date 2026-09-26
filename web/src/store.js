// 全局会话状态（轻量响应式单例，避免引入 Pinia）。
import { reactive } from 'vue'
import { fetchMe, isAuthError } from './api'

export const session = reactive({
  ready: false, // 是否已完成一次身份探测
  authenticated: false,
  me: null,
})

/** 探测当前会话；未登录时置 authenticated=false 而不抛出。 */
export async function refreshSession() {
  try {
    session.me = await fetchMe()
    session.authenticated = true
  } catch (err) {
    if (isAuthError(err)) {
      session.authenticated = false
      session.me = null
    } else {
      // 网络/服务端异常：不覆盖登录态，交由页面处理
      throw err
    }
  } finally {
    session.ready = true
  }
  return session.authenticated
}

/** 清空本地会话态。 */
export function clearSession() {
  session.authenticated = false
  session.me = null
}
