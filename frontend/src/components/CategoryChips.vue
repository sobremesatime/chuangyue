<script setup lang="ts">
// 分类筛选：胶囊 chips 横排，窄屏可横向滚动。
// 复用全局按钮类，选中态由 .cy-btn.is-active 提供。

import type { VideoCategory } from '@/types/api'

defineProps<{
  modelValue: number
  categories: VideoCategory[]
}>()

const emit = defineEmits<{ (e: 'update:modelValue', value: number): void }>()
</script>

<template>
  <div class="chips" role="group" aria-label="分类筛选">
    <button
      class="cy-btn cy-btn--secondary cy-btn--pill cy-btn--sm"
      :class="{ 'is-active': modelValue === 0 }"
      type="button"
      :aria-pressed="modelValue === 0"
      @click="emit('update:modelValue', 0)"
    >
      全部
    </button>

    <button
      v-for="category in categories"
      :key="category.id"
      class="cy-btn cy-btn--secondary cy-btn--pill cy-btn--sm"
      :class="{ 'is-active': modelValue === category.id }"
      type="button"
      :aria-pressed="modelValue === category.id"
      @click="emit('update:modelValue', category.id)"
    >
      {{ category.name }}
    </button>
  </div>
</template>

<style scoped>
.chips {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  overflow-x: auto;
  padding-bottom: var(--cy-space-1);
  scrollbar-width: none;
}

.chips::-webkit-scrollbar {
  display: none;
}

.chips > * {
  flex-shrink: 0;
}
</style>
