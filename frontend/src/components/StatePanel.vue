<script setup lang="ts">
// 空状态 / 错误状态面板。
//
// 首页与观看页原来各写一份、内边距还不一致，这里收敛成一个可复用组件：
// 空状态用月标轮廓，错误状态用线性警告图标，不用 emoji。

import AppIcon from '@/components/AppIcon.vue'

withDefaults(
  defineProps<{
    variant?: 'empty' | 'error'
    title: string
    description?: string
    actionLabel?: string
  }>(),
  { variant: 'empty', description: '', actionLabel: '' },
)

const emit = defineEmits<{ (e: 'action'): void }>()
</script>

<template>
  <div class="cy-state state-panel">
    <span class="state-panel__mark" :class="`state-panel__mark--${variant}`">
      <AppIcon :name="variant === 'error' ? 'alert' : 'moon'" :size="52" />
    </span>

    <p class="cy-state__title">{{ title }}</p>
    <p v-if="description" class="cy-state__desc">{{ description }}</p>

    <button
      v-if="actionLabel"
      class="cy-btn cy-btn--primary cy-btn--pill"
      type="button"
      @click="emit('action')"
    >
      {{ actionLabel }}
    </button>

    <slot />
  </div>
</template>

<style scoped>
.state-panel__mark {
  display: grid;
  place-items: center;
  width: 96px;
  height: 96px;
  color: var(--cy-text);
  opacity: 0.18;
}

.state-panel__mark--error {
  color: var(--cy-danger);
  opacity: 0.9;
}
</style>
