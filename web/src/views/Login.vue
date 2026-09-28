<script setup>
// 登录页：生产走「飞书授权登录」（authorize-url → 飞书授权页 → 回调建会话），
// 开发模式允许用 open_id 直接建立会话（DEV_MODE=true）。
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { fetchAuthorizeUrl } from '../api'
import { refreshSession } from '../store'

const router = useRouter()
const route = useRoute()
const openId = ref('')
const busy = ref(false)
const err = ref('')

// 路由守卫 / 免登回调携带的 error 参数 → 可读文案（不白屏、不静默）。
const ERROR_TEXT = {
  denied: '你取消了飞书授权。如需访问，请重新点击「用飞书账号登录」并同意授权。',
  sso_loop: '免登未完成（会话未能建立）。请重试；若反复失败，请联系管理员检查回调地址配置。',
  authorize_unavailable: '免登服务暂不可用：请检查服务端 JX_APP_ID / JX_OAUTH_REDIRECT_URI 配置。',
  probe_failed: '登录状态探测失败（网络或服务异常），请稍后重试。',
}
if (route.query.error) {
  err.value = ERROR_TEXT[route.query.error] || `登录失败：${route.query.error}`
}

// 登录页可选回跳目标：仅当 URL 带 ?redirect=（如深链 /login?redirect=/approval/PR-1）
// 时透传给 authorize-url（后端绑定 state Cookie、登录成功后 302 回跳）；
// 未带 ⇒ 不传，后端无目标时回落 /。★ 仅字符串形态，防 vue-router 的数组值。
function redirectTarget() {
  const q = route.query.redirect
  const v = Array.isArray(q) ? q[0] : q
  return typeof v === 'string' && v ? v : ''
}

/** 发起飞书免登：取授权页 URL 后整页跳转。 */
async function feishuLogin() {
  err.value = ''
  busy.value = true
  try {
    const data = await fetchAuthorizeUrl(redirectTarget() || undefined)
    if (!data || !data.authorize_url) throw new Error('authorize_url 为空')
    // 清防重入标记：用户主动重试时允许再次整页跳转。
    sessionStorage.removeItem('jx_sso_attempted')
    window.location.replace(data.authorize_url)
  } catch (e) {
    err.value = e && e.message ? `发起飞书免登失败：${e.message}` : '发起飞书免登失败'
    busy.value = false
  }
}

function gotoDevLogin() {
  err.value = ''
  const id = openId.value.trim()
  if (!id) {
    err.value = '请输入 open_id'
    return
  }
  busy.value = true
  // 开发模式下后端允许 ?open_id= 直接免登（见 httpapi.handleFeishuCallback）；
  // 带 ?redirect= 时同样支持登录后回跳（后端 DEV 直连路径接受该参数）。
  const target = redirectTarget()
  const extra = target ? `&redirect=${encodeURIComponent(target)}` : ''
  window.location.href = `/auth/feishu/callback?state=devlogi&open_id=${encodeURIComponent(id)}${extra}`
}

async function probe() {
  const ok = await refreshSession()
  if (ok) router.push('/dashboard')
  else err.value = '尚未登录：请点击「用飞书账号登录」。'
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-card">
      <h1>江熙新材审批系统</h1>
      <p>请使用飞书账号登录（open_id → 角色映射后放行）</p>

      <div class="toolbar" style="justify-content: center">
        <button class="primary" :disabled="busy" @click="feishuLogin">用飞书账号登录</button>
        <button class="ghost" @click="probe">我已登录，继续</button>
      </div>

      <hr style="border: none; border-top: 1px solid var(--border); margin: 20px 0" />

      <p class="muted" style="margin-bottom: 8px">开发模式（DEV_MODE=true）直连登录</p>
      <div class="toolbar" style="justify-content: center">
        <input v-model="openId" placeholder="open_id，如 ou_dev_001" @keyup.enter="gotoDevLogin" />
        <button class="ghost" :disabled="busy" @click="gotoDevLogin">进入</button>
      </div>
      <div v-if="err" class="error">{{ err }}</div>
    </div>
  </div>
</template>
