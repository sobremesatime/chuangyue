import { fileURLToPath, URL } from 'node:url'

import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// Vite 配置：开发期用 proxy 把 /api 与 /uploads 转发到 Go 网关，
// 这样浏览器视角下前后端同源，既没有跨域问题，HttpOnly Cookie（刷新令牌）也能正常写入。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    // 必须显式指定 host：默认绑定的是 localhost，而它在本机会解析成 IPv6 的 ::1，
    // 于是只监听 [::1]:5173 —— 用 127.0.0.1 访问会被直接拒绝（ERR_CONNECTION_REFUSED）。
    // 设为 true 同时监听 IPv4 / IPv6，localhost 与 127.0.0.1 都能打开；
    // 副作用是对局域网开放（同网段手机也能访问，便于真机调试；不需要则改回不设此项）。
    host: true,
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
      '/uploads': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
})
