# frontend · 前端（Vue 3 + Vite + TypeScript）

独立构建、独立部署，与后端通过 `api/openapi/` 的契约通信。

## 快速开始

```bash
cd frontend
npm install
npm run gen:types   # 由 OpenAPI 契约生成 TS 类型（首次或契约变更后执行）
npm run dev         # 开发服务器 http://localhost:5173
```

开发环境由 Vite `server.proxy` 把 `/api` 与 `/uploads` 转发到网关（默认 `http://127.0.0.1:8080`），
浏览器视角下前后端同源，因此没有跨域问题，HttpOnly Cookie（刷新令牌）也能正常写入。

## 常用命令

| 命令 | 作用 |
| --- | --- |
| `npm run dev` | 启动开发服务器（热更新） |
| `npm run build` | 类型检查 + 生产构建，产物在 `dist/` |
| `npm run preview` | 本地预览构建产物 |
| `npm run gen:types` | 从 `../api/openapi/chuangyue.v1.yaml` 生成 `src/types/openapi.d.ts` |

## 目录说明

| 目录 | 用途 |
| --- | --- |
| `public/` | 不参与构建的静态资源 |
| `src/api/` | axios 实例与接口封装 |
| `src/assets/` | 参与构建的静态资源 |
| `src/components/` | 通用组件（视频卡片、分页器） |
| `src/composables/` | 组合式函数（预留） |
| `src/layouts/` | 页面布局（主站布局、认证页布局） |
| `src/router/` | 路由与导航守卫 |
| `src/stores/` | Pinia 状态管理 |
| `src/styles/` | 全局样式与设计变量 |
| `src/types/` | TS 类型（核心实体由 OpenAPI 契约生成） |
| `src/utils/` | 工具函数 |
| `src/views/` | 页面 |

## 与后端的约定

- 所有请求走 `/api/v1/*`，开发环境由 Vite `server.proxy` 转发到网关。
- **Access Token 只存内存**（`src/api/request.ts` 的模块变量），不落 localStorage，降低 XSS 风险。
- **Refresh Token 由后端写入 HttpOnly Cookie**，前端读不到也不需要读。
- 页面刷新后由 `userStore.bootstrap()` 调用 `POST /auth/refresh` 恢复登录态，
  路由守卫在首次导航时 await 它，因此不会出现「先显示未登录再闪成已登录」。
- 访问令牌过期（HTTP 401）时，`request.ts` 的响应拦截器会自动刷新一次并重放原请求，
  并发请求只会触发一次刷新，业务代码无需关心。
- 核心实体类型（`User` / `Video` / `TokenPair` 等）由 OpenAPI 契约生成，不手写；
  契约变更后重新执行 `npm run gen:types`。

## 设计说明

- 视觉风格参考 B 站：浅色底、粉色主色（`#fb7299`）、卡片网格、圆角搜索框。
- 设计变量集中在 `src/styles/main.css` 的 `:root`，改主题只需改这一处。
- 视频封面缺失时用「标题首字 + 按标题哈希出的渐变色块」兜底，不引入占位图。
- 搜索框已就位但处于禁用态——搜索服务在后续版本上线，接口就绪后打开即可。

## 已实现页面

| 路由 | 页面 | 说明 |
| --- | --- | --- |
| `/` | 首页 | 视频列表（分页、骨架屏、空状态、错误重试） |
| `/videos/:id` | 视频详情 | 播放器 + 标题 + 统计 + 简介 |
| `/login` | 登录 | 用户名密码登录 |
| `/register` | 注册 | 注册后跳回登录页并带入用户名 |
