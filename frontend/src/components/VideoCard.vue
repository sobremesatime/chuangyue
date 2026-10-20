<script setup lang="ts">
// 视频卡片：无边框、无阴影，只有 16:9 圆角封面 + 文字层级。
//
// hover 时封面淡入遮罩与播放图标、封面轻微放大（scale 1.03），卡片本身不位移
// —— 位移是另一种视频站的做法，这里跟 YouTube 保持一致。

import { computed } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import type { VideoCardItem } from '@/types/api'
import { formatCount, formatDuration, formatRelativeDate } from '@/utils/format'

const props = defineProps<{ video: VideoCardItem }>()

// 无封面时用星紫纯色平涂 + 月白首字，不做随机渐变（Logo 规范要求纯色平涂）
const initial = computed(() => props.video.title.trim().charAt(0) || '窗')
</script>

<template>
  <RouterLink class="card" :to="{ name: 'video-detail', params: { id: video.id } }">
    <div class="card__cover">
      <img
        v-if="video.cover_url"
        class="card__img"
        :src="video.cover_url"
        :alt="video.title"
        loading="lazy"
      />
      <div v-else class="card__fallback" aria-hidden="true">
        <span>{{ initial }}</span>
      </div>

      <span v-if="video.duration > 0" class="card__duration cy-num">
        {{ formatDuration(video.duration) }}
      </span>

      <span class="card__play" aria-hidden="true">
        <AppIcon name="play" :size="22" />
      </span>
    </div>

    <h3 class="card__title cy-clamp-2">{{ video.title }}</h3>
    <p class="card__meta cy-caption">
      窗月官方 · <span class="cy-num">{{ formatCount(video.view_count) }}</span> 次播放 ·
      {{ formatRelativeDate(video.created_at) }}
    </p>
  </RouterLink>
</template>

<style scoped>
.card {
  display: block;
  border-radius: var(--cy-radius-md);
}

.card__cover {
  position: relative;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-md);
  overflow: hidden;
  background: var(--cy-surface-2);
}

.card__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform var(--cy-dur-base) var(--cy-ease);
}

.card__cover::after {
  content: '';
  position: absolute;
  inset: 0;
  background: var(--cy-overlay);
  opacity: 0;
  transition: opacity var(--cy-dur-base) var(--cy-ease);
}

.card:hover .card__img,
.card:focus-visible .card__img {
  transform: scale(1.03);
}

.card:hover .card__cover::after,
.card:focus-visible .card__cover::after {
  opacity: 1;
}

.card__fallback {
  display: grid;
  place-items: center;
  width: 100%;
  height: 100%;
  background: var(--cy-primary);
  color: var(--cy-on-primary);
  font-size: 40px;
  font-weight: 600;
  letter-spacing: 2px;
}

.card__duration {
  position: absolute;
  right: var(--cy-space-1);
  bottom: var(--cy-space-1);
  z-index: 1;
  height: 18px;
  padding: 0 var(--cy-space-1);
  border-radius: var(--cy-radius-xs);
  background: var(--cy-media-badge-bg);
  color: var(--cy-media-badge-text);
  font-size: 12px;
  line-height: 18px;
}

.card__play {
  position: absolute;
  top: 50%;
  left: 50%;
  z-index: 1;
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  margin: -22px 0 0 -22px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-media-badge-bg);
  color: var(--cy-media-badge-text);
  opacity: 0;
  transition: opacity var(--cy-dur-base) var(--cy-ease);
}

.card:hover .card__play,
.card:focus-visible .card__play {
  opacity: 1;
}

.card__title {
  margin-top: var(--cy-space-3);
  color: var(--cy-text);
  font-size: 14px;
  line-height: 22px;
  font-weight: 500;
  min-height: 44px;
}

.card__meta {
  margin-top: var(--cy-space-1);
}
</style>
