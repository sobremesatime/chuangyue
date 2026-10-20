<script setup lang="ts">
// 未知路径的兜底页。以前是静默重定向首页，用户不知道自己输错了地址。

import StatePanel from '@/components/StatePanel.vue'
</script>

<template>
  <div class="not-found cy-container">
    <StatePanel title="页面走丢了" description="这个地址下没有内容，也许链接已经失效。">
      <RouterLink class="cy-btn cy-btn--primary cy-btn--pill" :to="{ name: 'home' }">
        回到首页
      </RouterLink>
    </StatePanel>
  </div>
</template>

<style scoped>
.not-found {
  padding-top: var(--cy-space-6);
  padding-bottom: var(--cy-space-7);
}
</style>
