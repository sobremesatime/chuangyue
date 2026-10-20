// 路由表与全局守卫。
//
// 守卫职责：
//   - 每次导航前调用 userStore.bootstrap()（幂等），用刷新令牌恢复登录态；
//   - requiresAuth / requiresAdmin：后续管理员页面（上传、看板）直接加 meta 即可。
//
// 登录与注册不再有独立页面，需要登录时由守卫就地唤出顶栏那一个登录弹框，
// 用户不会离开当前浏览的内容。
//
// 每页标题由 meta.title 驱动，渲染成「<标题> · 窗月」；观看页会在拿到视频后
// 把标题覆盖成视频名。

import { createRouter, createWebHistory } from 'vue-router'

import { useAuthModalStore } from '@/stores/authModal'
import { useUserStore } from '@/stores/user'

declare module 'vue-router' {
  interface RouteMeta {
    /** 需要登录 */
    requiresAuth?: boolean
    /** 需要管理员角色 */
    requiresAdmin?: boolean
    /** 浏览器标题（不含品牌后缀） */
    title?: string
  }
}

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      component: () => import('@/layouts/DefaultLayout.vue'),
      children: [
        {
          path: '',
          name: 'home',
          component: () => import('@/views/HomeView.vue'),
          meta: { title: '首页' },
        },
        {
          path: 'videos/:id',
          name: 'video-detail',
          component: () => import('@/views/VideoDetailView.vue'),
          meta: { title: '观看' },
        },
        {
          path: 'trending',
          name: 'trending',
          component: () => import('@/views/TrendingView.vue'),
          meta: { title: '热门' },
        },
        {
          path: 'profile',
          name: 'profile',
          component: () => import('@/views/ProfileView.vue'),
          meta: { title: '个人中心', requiresAuth: true },
        },
        {
          path: 'settings',
          name: 'settings',
          component: () => import('@/views/AccountSettingsView.vue'),
          meta: { title: '账号设置', requiresAuth: true },
        },
        {
          path: 'me/likes',
          name: 'my-likes',
          component: () => import('@/views/MyVideosView.vue'),
          meta: { title: '赞过的视频', requiresAuth: true },
        },
        {
          path: 'me/favorites',
          name: 'my-favorites',
          component: () => import('@/views/MyVideosView.vue'),
          meta: { title: '我的收藏', requiresAuth: true },
        },
        {
          path: 'me/watch-later',
          name: 'my-watch-later',
          component: () => import('@/views/MyVideosView.vue'),
          meta: { title: '稍后再看', requiresAuth: true },
        },
        {
          path: 'me/history',
          name: 'my-history',
          component: () => import('@/views/MyVideosView.vue'),
          meta: { title: '观看历史', requiresAuth: true },
        },
        {
          path: 'admin/upload',
          name: 'admin-upload',
          component: () => import('@/views/UploadView.vue'),
          meta: { title: '投稿', requiresAuth: true, requiresAdmin: true },
        },
        {
          path: 'admin/videos',
          name: 'admin-videos',
          component: () => import('@/views/AdminVideosView.vue'),
          meta: { title: '视频管理', requiresAuth: true, requiresAdmin: true },
        },
        {
          path: 'admin/comments',
          name: 'admin-comments',
          component: () => import('@/views/AdminCommentsView.vue'),
          meta: { title: '评论管理', requiresAuth: true, requiresAdmin: true },
        },
        {
          path: 'admin/danmaku',
          name: 'admin-danmaku',
          component: () => import('@/views/AdminDanmakuView.vue'),
          meta: { title: '弹幕管理', requiresAuth: true, requiresAdmin: true },
        },
        {
          path: 'admin/reports',
          name: 'admin-reports',
          component: () => import('@/views/AdminReportsView.vue'),
          meta: { title: '举报处理', requiresAuth: true, requiresAdmin: true },
        },
        {
          path: 'admin/users',
          name: 'admin-users',
          component: () => import('@/views/AdminUsersView.vue'),
          meta: { title: '用户管理', requiresAuth: true, requiresAdmin: true },
        },
        {
          path: ':pathMatch(.*)*',
          name: 'not-found',
          component: () => import('@/views/NotFoundView.vue'),
          meta: { title: '页面不存在' },
        },
      ],
    },
  ],
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach(async (to, from) => {
  const userStore = useUserStore()
  await userStore.bootstrap()

  if (to.meta.requiresAuth && !userStore.isLoggedIn) {
    // 不跳转登录页，就地弹出登录框；首次进入（没有来源页）时先回首页，
    // 否则取消导航会停在空白页
    useAuthModalStore().openModal('login')
    return from.name ? false : { name: 'home' }
  }
  if (to.meta.requiresAdmin && !userStore.isAdmin) {
    return { name: 'home' }
  }
  return true
})

router.afterEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} · 窗月` : '窗月'
})

export default router
