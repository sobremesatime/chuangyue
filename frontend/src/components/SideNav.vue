<script setup lang="ts">
// 侧边导航：完整 240px / 图标条 72px / 移动端抽屉三种形态。
//
// 尚未实现的入口一律置灰并标注「后续版本」，不做成可点但无反应的死按钮。

import { computed } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import { useUserStore } from '@/stores/user'

const props = defineProps<{
  collapsed: boolean
  drawer: boolean
  open: boolean
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

const userStore = useUserStore()

interface NavItem {
  label: string
  icon:
    | 'home'
    | 'trending'
    | 'star'
    | 'thumbs-up'
    | 'clock'
    | 'later'
    | 'grid'
    | 'playlist'
    | 'upload'
    | 'comment'
    | 'danmaku'
    | 'report'
    | 'id-card'
  to?: { name: string }
}

interface NavGroup {
  title: string
  ready: NavItem[]
  comingSoon: NavItem[]
}

const groups = computed<NavGroup[]>(() => {
  const list: NavGroup[] = [
    {
      title: '浏览',
      ready: [
        { label: '首页', icon: 'home', to: { name: 'home' } },
        { label: '热门', icon: 'trending', to: { name: 'trending' } },
      ],
      comingSoon: [],
    },
    {
      title: '我',
      ready: [
        { label: '我的收藏', icon: 'star', to: { name: 'my-favorites' } },
        { label: '赞过的视频', icon: 'thumbs-up', to: { name: 'my-likes' } },
        { label: '稍后再看', icon: 'later', to: { name: 'my-watch-later' } },
        { label: '观看历史', icon: 'clock', to: { name: 'my-history' } },
      ],
      comingSoon: [],
    },
  ]

  if (userStore.isAdmin) {
    list.push({
      title: '管理',
      ready: [
        { label: '投稿', icon: 'upload', to: { name: 'admin-upload' } },
        { label: '视频管理', icon: 'grid', to: { name: 'admin-videos' } },
        { label: '合集管理', icon: 'playlist', to: { name: 'admin-collections' } },
        { label: '评论管理', icon: 'comment', to: { name: 'admin-comments' } },
        { label: '弹幕管理', icon: 'danmaku', to: { name: 'admin-danmaku' } },
        { label: '举报处理', icon: 'report', to: { name: 'admin-reports' } },
        { label: '用户管理', icon: 'id-card', to: { name: 'admin-users' } },
      ],
      comingSoon: [],
    })
  }

  return list
})
</script>

<template>
  <div
    v-if="props.drawer && props.open"
    class="sidenav__scrim"
    aria-hidden="true"
    @click="emit('close')"
  />
  <aside
    class="sidenav"
    :class="{
      'is-collapsed': props.collapsed,
      'is-drawer': props.drawer,
      'is-open': props.open,
    }"
  >
    <nav class="sidenav__nav" aria-label="主导航">
      <div v-for="group in groups" :key="group.title" class="sidenav__group">
        <p class="sidenav__group-title">{{ group.title }}</p>

        <RouterLink
          v-for="item in group.ready"
          :key="item.label"
          class="sidenav__item"
          :to="item.to!"
        >
          <span class="sidenav__rail" aria-hidden="true" />
          <AppIcon :name="item.icon" :size="20" />
          <span class="sidenav__label">{{ item.label }}</span>
        </RouterLink>

        <span
          v-for="item in group.comingSoon"
          :key="item.label"
          class="sidenav__item is-disabled"
          :title="`${item.label}将在后续版本开放`"
          aria-disabled="true"
        >
          <AppIcon :name="item.icon" :size="20" />
          <span class="sidenav__label">{{ item.label }}</span>
          <span class="sidenav__tag">后续版本</span>
        </span>
      </div>
    </nav>
  </aside>
</template>

<style scoped>
/*
 * 固定定位而不是 sticky：sticky 只能在自己的包含块内吸顶，
 * 而包含块高度取决于页面内容，内容一短导航就会被页面"卷走"。
 * 固定定位与内容高度彻底无关，任何情况下都不会随滚动消失。
 * 它脱离文档流后的横向占位由布局的 padding-left 负责（见 DefaultLayout）。
 */
.sidenav {
  position: fixed;
  top: var(--cy-header-h);
  bottom: 0;
  left: 0;
  z-index: 80;
  width: var(--cy-nav-w);
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: var(--cy-space-3) var(--cy-space-3) var(--cy-space-5);
  transition: width var(--cy-dur-base) var(--cy-ease);
}

.sidenav.is-collapsed {
  width: var(--cy-nav-w-collapsed);
}

.sidenav__nav {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-4);
}

.sidenav__group-title {
  padding: 0 var(--cy-space-3) var(--cy-space-2);
  font-size: 12px;
  line-height: 18px;
  font-weight: 600;
  color: var(--cy-text-muted);
}

.sidenav__item {
  position: relative;
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  height: 44px;
  padding: 0 var(--cy-space-3);
  border-radius: var(--cy-radius-sm);
  color: var(--cy-text-secondary);
  font-size: 14px;
  transition: background-color var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease);
}

.sidenav__item:hover:not(.is-disabled) {
  background: var(--cy-surface-2);
  color: var(--cy-text);
}

/* 选中态：浅紫底 + 浅星紫文字 + 左侧 3px 品牌紫指示条 */
.sidenav__item.router-link-exact-active {
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
  font-weight: 500;
}

.sidenav__rail {
  display: none;
  position: absolute;
  left: 0;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 22px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-primary);
}

.sidenav__item.router-link-exact-active .sidenav__rail {
  display: block;
}

.sidenav__label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sidenav__tag {
  margin-left: auto;
  font-size: 11px;
  line-height: 16px;
  color: var(--cy-text-muted);
  opacity: 0.8;
}

.sidenav__item.is-disabled {
  color: var(--cy-text-muted);
  opacity: 0.55;
  cursor: not-allowed;
}

/* 图标条形态：只留图标，文字与标签隐藏 */
.sidenav.is-collapsed .sidenav__group-title,
.sidenav.is-collapsed .sidenav__label,
.sidenav.is-collapsed .sidenav__tag {
  display: none;
}

.sidenav.is-collapsed .sidenav__item {
  justify-content: center;
  padding: 0;
}

.sidenav__scrim {
  position: fixed;
  inset: var(--cy-header-h) 0 0;
  z-index: 90;
  background: var(--cy-overlay);
}

/* 移动端：抽屉从左侧滑出，遮罩点击关闭 */
.sidenav.is-drawer {
  z-index: 95;
  background: var(--cy-bg);
  border-right: 1px solid var(--cy-border);
  transform: translateX(-100%);
  transition: transform var(--cy-dur-base) var(--cy-ease);
}

.sidenav.is-drawer.is-open {
  transform: none;
}
</style>
