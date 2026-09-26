<script setup>
// 登录页：生产走飞书授权回调，开发模式允许用 open_id 直接建立会话（DEV_MODE=true）。
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { refreshSession } from '../store'

const router = useRouter()
const openId = ref('')
const busy = ref(false)
const err = ref('')

function gotoDevLogin() {
  err.value = ''
  const id = openId.value.trim()
  if (!id) {
    err.value = '请输入 open_id'
    return
  }
  busy.value = true
  // 开发模式下后端允许 ?open_id= 直连免登（见 httpapi.handleFeishuCallback）
  window.location.href = `/auth/feishu/callback?state=devlogi&open_id=${encodeURIComponent(id)}`
}

async function probe() {
  const ok = await refreshSession()
  if (ok) router.push('/dashboard')
  else err.value = '尚未登录：请通过飞书免登进入。'
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-card">
      <h1>江熙 · 采购与费用审批平台</h1>
      <p>请通过飞书免登进入（open_id → 角色映射后放行）</p>

      <div class="toolbar" style="justify-content: center">
        <button class="primary" @click="probe">我已登录，继续</button>
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
