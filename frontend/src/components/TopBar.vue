<script setup lang="ts">
// 顶栏：品牌 + 搜索 + 用户区。高度 56px，吸顶，底色与页面底分开一档，滚动后才出现 1px 底边。
//
// 未登录时右区只有一个「登录」按钮，点击唤出登录弹框（登录 / 注册都在弹框里完成）；
// 已登录时右区是一枚头像，点击展开账号下拉，个人中心、账号设置两个独立页面与退出登录都在框里，
// 顶栏不再出现用户名文本。
//
// 搜索栏按 YouTube 的样子放在顶栏中部：胶囊整体 + 左侧输入 + 分隔线 + 右侧搜索按钮。
// 提交后跳到首页并写入 ?q=，由首页以 keyword 参数请求服务端搜索。

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import AppIcon from '@/components/AppIcon.vue'
import NotificationBell from '@/components/NotificationBell.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { useAuthModalStore } from '@/stores/authModal'
import { useThemeStore } from '@/stores/theme'
import { useUserStore } from '@/stores/user'

defineProps<{ navOpen: boolean }>()
const emit = defineEmits<{ (e: 'toggle-nav'): void }>()

const userStore = useUserStore()
const themeStore = useThemeStore()
const authModal = useAuthModalStore()
const router = useRouter()
const route = useRoute()

/** 账号下拉的显隐；点击头像切换，点框外或按 Escape 收起 */
const menuOpen = ref(false)
const accountRef = ref<HTMLElement | null>(null)
const avatarRef = ref<HTMLButtonElement | null>(null)
const scrolled = ref(false)
const searchInput = ref(typeof route.query.q === 'string' ? route.query.q : '')

const displayName = computed(
  () => userStore.profile?.nickname || userStore.profile?.username || '',
)
const themeIcon = computed(() => (themeStore.resolved === 'dark' ? 'sun' : 'moon'))
const themeLabel = computed(() =>
  themeStore.resolved === 'dark' ? '切换到浅色模式' : '切换到深色模式',
)

// 从别处（如浏览器前进后退）回到首页时，输入框跟随 ?q= 同步
watch(
  () => route.query.q,
  (value) => {
    searchInput.value = typeof value === 'string' ? value : ''
  },
)

function onScroll(): void {
  scrolled.value = window.scrollY > 0
}

async function handleSearch(): Promise<void> {
  const keyword = searchInput.value.trim()
  await router.push({ name: 'home', query: keyword ? { q: keyword } : {} })
}

function closeMenu(): void {
  menuOpen.value = false
}

/** 点在下拉框与头像之外就收起；头像点击本身也会冒泡到这里，用容器判断兜住 */
function onDocumentClick(event: MouseEvent): void {
  if (!menuOpen.value) {
    return
  }
  const target = event.target as Node | null
  if (target && accountRef.value?.contains(target)) {
    return
  }
  closeMenu()
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && menuOpen.value) {
    closeMenu()
    avatarRef.value?.focus()
  }
}

/** 个人中心是独立页面，进页面后把下拉收起 */
async function goProfile(): Promise<void> {
  closeMenu()
  await router.push({ name: 'profile' })
}

/** 账号设置同样是独立页面，进页面后把下拉收起 */
async function goSettings(): Promise<void> {
  closeMenu()
  await router.push({ name: 'settings' })
}

/** 「退出登录」：登出后收起下拉，若当前页需要登录则回首页 */
async function handleLogout(): Promise<void> {
  closeMenu()
  await userStore.logout()
  if (route.meta.requiresAuth) {
    await router.push({ name: 'home' })
  }
}

onMounted(() => {
  window.addEventListener('scroll', onScroll, { passive: true })
  document.addEventListener('click', onDocumentClick)
  document.addEventListener('keydown', onKeydown)
  onScroll()
})

onBeforeUnmount(() => {
  window.removeEventListener('scroll', onScroll)
  document.removeEventListener('click', onDocumentClick)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <header class="topbar" :class="{ 'is-scrolled': scrolled }">
    <div class="topbar__inner">
      <div class="topbar__lead">
        <button
          class="cy-btn cy-btn--icon cy-btn--ghost topbar__icon"
          type="button"
          :aria-expanded="navOpen"
          aria-label="切换导航栏"
          @click="emit('toggle-nav')"
        >
          <AppIcon name="menu" :size="22" />
        </button>

        <RouterLink class="topbar__brand" :to="{ name: 'home' }">
          <img class="topbar__mark" src="/icon.svg" alt="" width="30" height="30" />
          <span class="topbar__name">窗月</span>
        </RouterLink>
      </div>

      <form class="topbar__search" role="search" @submit.prevent="handleSearch">
        <input
          v-model="searchInput"
          class="topbar__search-input"
          type="search"
          placeholder="搜索视频"
          aria-label="搜索视频"
        />
        <span class="topbar__search-divider" aria-hidden="true" />
        <button class="topbar__search-btn" type="submit" aria-label="搜索">
          <AppIcon name="search" :size="20" />
        </button>
      </form>

      <div class="topbar__actions">
        <button
          class="cy-btn cy-btn--icon cy-btn--ghost topbar__icon"
          type="button"
          :aria-label="themeLabel"
          :title="themeLabel"
          @click="themeStore.toggle()"
        >
          <AppIcon :name="themeIcon" :size="20" />
        </button>

        <template v-if="userStore.isLoggedIn">
          <NotificationBell />

          <div ref="accountRef" class="topbar__account">
            <button
              ref="avatarRef"
              class="topbar__user"
              type="button"
              aria-haspopup="menu"
              :aria-expanded="menuOpen"
              aria-label="账号菜单"
              title="账号菜单"
              @click="menuOpen = !menuOpen"
            >
              <UserAvatar :src="userStore.profile?.avatar" :size="32" :alt="displayName" />
            </button>

            <Transition name="topbar-menu">
              <div v-if="menuOpen" class="topbar__menu" role="menu">
                <div class="topbar__menu-head">
                  <UserAvatar :src="userStore.profile?.avatar" :size="36" :alt="displayName" />
                  <div class="topbar__menu-id">
                    <p class="topbar__menu-name cy-clamp-1">{{ displayName }}</p>
                    <p class="topbar__menu-meta cy-clamp-1">@{{ userStore.profile?.username }}</p>
                  </div>
                </div>

                <button class="topbar__menu-item" type="button" role="menuitem" @click="goProfile">
                  <AppIcon name="user" :size="18" />
                  <span>个人中心</span>
                </button>
                <button class="topbar__menu-item" type="button" role="menuitem" @click="goSettings">
                  <AppIcon name="id-card" :size="18" />
                  <span>账号设置</span>
                </button>
                <button class="topbar__menu-item" type="button" role="menuitem" @click="handleLogout">
                  <AppIcon name="logout" :size="18" />
                  <span>退出登录</span>
                </button>
              </div>
            </Transition>
          </div>
        </template>

        <button
          v-else
          class="cy-btn cy-btn--primary"
          type="button"
          @click="authModal.openModal('login')"
        >
          登录
        </button>
      </div>
    </div>
  </header>
</template>

<style scoped>
.topbar {
  position: sticky;
  top: 0;
  z-index: 100;
  height: var(--cy-header-h);
  /* 页面底改成纯白后，顶栏单独留一档纸白，两行之间形成淡淡的层次 */
  background: var(--cy-header-bg);
  border-bottom: 1px solid transparent;
  transition: border-color var(--cy-dur-base) var(--cy-ease);
}

.topbar.is-scrolled {
  border-bottom-color: var(--cy-border);
}

.topbar__inner {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  height: 100%;
  padding: 0 var(--cy-space-4);
}

/*
 * 左右两区给同一份伸缩基数（flex: 1 1 0），两者始终等宽，
 * 夹在中间且不参与伸缩的搜索栏因此恒落在顶栏正中。
 * 之前用 margin: 0 auto 会与右区的 margin-left: auto 三分剩余空间，把搜索栏推偏。
 */
.topbar__lead,
.topbar__actions {
  display: flex;
  align-items: center;
  flex: 1 1 0;
  min-width: 0;
}

.topbar__lead {
  gap: var(--cy-space-3);
}

.topbar__actions {
  justify-content: flex-end;
  gap: var(--cy-space-2);
}

.topbar__icon {
  color: var(--cy-text-secondary);
}

.topbar__icon:disabled {
  cursor: not-allowed;
}

.topbar__brand {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  flex-shrink: 0;
  padding: var(--cy-space-1) var(--cy-space-2);
  border-radius: var(--cy-radius-sm);
}

.topbar__mark {
  width: 30px;
  height: 30px;
  border-radius: var(--cy-radius-logo);
}

/* 品牌标用专用字体（站酷快乐体子集），只覆盖「窗月」两字，字面本身自带年轻感 */
.topbar__name {
  font-family: var(--cy-font-brand);
  font-size: 20px;
  line-height: 28px;
  font-weight: 400;
  letter-spacing: 2px;
  color: var(--cy-text);
}

/* 搜索：YouTube 式胶囊 —— 外层描边包裹输入框与搜索按钮，中间一条分隔线 */
.topbar__search {
  flex: 0 1 640px;
  max-width: min(640px, 40%);
  display: flex;
  align-items: center;
  height: 40px;
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-full);
  background: var(--cy-surface-2);
  overflow: hidden;
  transition: border-color var(--cy-dur-fast) var(--cy-ease),
    box-shadow var(--cy-dur-fast) var(--cy-ease);
}

.topbar__search:focus-within {
  border-color: var(--cy-primary);
  box-shadow: 0 0 0 3px var(--cy-primary-soft);
}

.topbar__search-input {
  flex: 1;
  min-width: 0;
  height: 100%;
  padding: 0 var(--cy-space-4);
  border: none;
  background: transparent;
  color: var(--cy-text);
  font-size: 14px;
  outline: none;
}

/* 焦点态由外层容器统一表达，这里压掉全局 :focus-visible 圆环 */
.topbar__search-input:focus-visible {
  outline: none;
}

.topbar__search-input::placeholder {
  color: var(--cy-text-muted);
}

/* 去掉 Chrome 对 type=search 自带的清除叉号，避免和右侧按钮重复 */
.topbar__search-input::-webkit-search-cancel-button {
  appearance: none;
}

.topbar__search-divider {
  flex-shrink: 0;
  width: 1px;
  height: 24px;
  background: var(--cy-border);
}

.topbar__search-btn {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 64px;
  height: 100%;
  color: var(--cy-text-secondary);
  transition: background-color var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease);
}

.topbar__search-btn:hover {
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

/* 账号区：头像按钮 + 绝对定位的下拉框 */
.topbar__account {
  position: relative;
}

.topbar__menu {
  position: absolute;
  top: calc(100% + var(--cy-space-2));
  right: 0;
  z-index: 120;
  min-width: 240px;
  padding: var(--cy-space-2);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-lg);
  background: var(--cy-surface);
  box-shadow: var(--cy-shadow-pop);
}

.topbar__menu-head {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  padding: var(--cy-space-2) var(--cy-space-3) var(--cy-space-3);
  margin-bottom: var(--cy-space-1);
  border-bottom: 1px solid var(--cy-border);
}

.topbar__menu-id {
  min-width: 0;
}

.topbar__menu-name {
  font-size: 14px;
  line-height: 20px;
  font-weight: 600;
}

.topbar__menu-meta {
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
}

.topbar__menu-item {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  width: 100%;
  padding: var(--cy-space-2) var(--cy-space-3);
  border-radius: var(--cy-radius-sm);
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
  text-align: left;
  transition: background-color var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease);
}

.topbar__menu-item:hover {
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

/* 下拉淡入，并从上方轻推下来 */
.topbar-menu-enter-active,
.topbar-menu-leave-active {
  transition: opacity var(--cy-dur-fast) var(--cy-ease),
    transform var(--cy-dur-fast) var(--cy-ease);
}

.topbar-menu-enter-from,
.topbar-menu-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}

/* 头像按钮：40px 命中区包住 32px 头像，悬停用一圈浅紫表达可点 */
.topbar__user {
  display: grid;
  place-items: center;
  width: 40px;
  height: 40px;
  border-radius: var(--cy-radius-full);
  transition: box-shadow var(--cy-dur-fast) var(--cy-ease);
}

.topbar__user:hover {
  box-shadow: 0 0 0 3px var(--cy-primary-soft);
}

/* 窄屏下两侧内容比可用空间还宽，此时退回按内容定宽，宁可略偏也不能挤在一起 */
@media (max-width: 1199px) {
  .topbar__lead,
  .topbar__actions {
    min-width: auto;
  }
}

@media (max-width: 599px) {
  .topbar__search-btn {
    width: 48px;
  }
}
</style>
