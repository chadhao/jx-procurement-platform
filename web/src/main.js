// 应用入口：装配 Vue3 + 路由 + 全局样式。
import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import './styles.css'

createApp(App).use(router).mount('#app')
