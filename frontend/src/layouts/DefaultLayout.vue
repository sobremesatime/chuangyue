<script setup lang="ts">
// 默认布局：顶栏 + 侧边导航 + 内容区 + 页脚。
//
// 侧边导航有三种形态（§3.7 断点）：
//   ≥900px   常驻，用户可手动收起（240px ↔ 72px）
//   600–899px 强制图标条 72px，腾出内容宽度
//   <600px   收进抽屉，由顶栏汉堡按钮唤出
//
// 导航是固定定位的，不占文档流；内容区与页脚用左内边距为它让出位置，
// 由根节点上的状态类统一切换，避免两处各写一套宽度。

import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import SideNav from '@/components/SideNav.vue'
import TopBar from '@/components/TopBar.vue'

// 页脚备案信息。按监管要求，ICP 备案号必须展示在网站首页，并链接到工信部备案系统；
// 公安联网备案通过后，再在下面追加一行「公安备案号 + 链接」，两者不能互相替代。
const ICP_BEIAN = '赣ICP备2026028308号'
const ICP_LINK = 'https://beian.miit.gov.cn'

const navCollapsed = ref(false)
const drawerOpen = ref(false)
const viewport = ref(window.innerWidth)

const isDrawer = computed(() => viewport.value < 600)
const effectiveCollapsed = computed(() => {
  if (isDrawer.value) {
    return false
  }
  if (viewport.value < 900) {
    return true
  }
  return navCollapsed.value
})

function onResize(): void {
  viewport.value = window.innerWidth
  if (!isDrawer.value) {
    drawerOpen.value = false
  }
}

function toggleNav(): void {
  if (isDrawer.value) {
    drawerOpen.value = !drawerOpen.value
    return
  }
  navCollapsed.value = !navCollapsed.value
}

onMounted(() => {
  window.addEventListener('resize', onResize)
  onResize()
})

onBeforeUnmount(() => window.removeEventListener('resize', onResize))
</script>

<template>
  <div
    class="layout"
    :class="{ 'is-nav-collapsed': !isDrawer && effectiveCollapsed, 'is-nav-drawer': isDrawer }"
  >
    <TopBar :nav-open="drawerOpen" @toggle-nav="toggleNav" />

    <div class="layout__body">
      <SideNav
        :collapsed="effectiveCollapsed"
        :drawer="isDrawer"
        :open="drawerOpen"
        @close="drawerOpen = false"
      />

      <main class="layout__main">
        <RouterView />
      </main>
    </div>

    <footer class="layout__footer">
      <div class="cy-container">
        <span>© 2026 窗月 CHUANGYUE</span>
        <span class="layout__footer-sep" aria-hidden="true">·</span>
        <a
          class="layout__footer-link"
          :href="ICP_LINK"
          target="_blank"
          rel="noopener noreferrer nofollow"
        >{{ ICP_BEIAN }}</a>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  flex-direction: column;
  min-height: 100%;
}

.layout__body {
  display: flex;
  flex: 1;
  align-items: flex-start;
}

.layout__main {
  flex: 1;
  min-width: 0;
}

.layout__footer {
  padding: var(--cy-space-5) 0 var(--cy-space-6);
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
  text-align: center;
}

/* 备案号是行政要求展示的信息，保持与版权信息同色，只在悬停时提高对比度 */
.layout__footer-link {
  color: inherit;
  text-decoration: none;
}

.layout__footer-link:hover {
  color: var(--cy-text);
  text-decoration: underline;
}

.layout__footer-sep {
  margin: 0 var(--cy-space-2);
}

/* 固定导航的横向占位。放在页脚规则之后，确保 padding-left 不被上面的 padding 简写覆盖 */
.layout__body,
.layout__footer {
  padding-left: var(--cy-nav-w);
  transition: padding-left var(--cy-dur-base) var(--cy-ease);
}

.layout.is-nav-collapsed .layout__body,
.layout.is-nav-collapsed .layout__footer {
  padding-left: var(--cy-nav-w-collapsed);
}

.layout.is-nav-drawer .layout__body,
.layout.is-nav-drawer .layout__footer {
  padding-left: 0;
}
</style>
