import { createPinia } from 'pinia'
import { createApp } from 'vue'

import App from './App.vue'
import router from './router'
import { useThemeStore } from './stores/theme'
import './styles/main.css'

const app = createApp(App)
const pinia = createPinia()

// 注意顺序：先装 Pinia，路由守卫里才能使用 store
app.use(pinia)
app.use(router)

// 主题必须在挂载前应用，否则首屏会闪一下默认皮肤
useThemeStore(pinia).init()

app.mount('#app')
