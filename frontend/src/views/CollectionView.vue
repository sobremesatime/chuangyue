<script setup lang="ts">
// 合集详情页 /collection/:id：封面 + 标题 + 简介 + 视频网格。
//
// 与视频详情页的分工：这里只负责「按合集内顺序把视频列出来」，
// 点卡片进入播放页；播放与连播仍由观看页负责，避免两处各写一套播放逻辑。
//
// 数据来自 GET /collections/{id}，videos 已经按合集顺序返回，前端不再排序。

import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { getCollection } from '@/api/collection'
import { toErrorMessage } from '@/api/request'
import StatePanel from '@/components/StatePanel.vue'
import VideoCard from '@/components/VideoCard.vue'
import VideoCardSkeleton from '@/components/VideoCardSkeleton.vue'
import type { CollectionDetail } from '@/types/api'
import { formatDate } from '@/utils/format'

const route = useRoute()

const collection = ref<CollectionDetail | null>(null)
const loading = ref(true)
const error = ref('')

const collectionId = computed(() => String(route.params.id))

/** 无封面时的平涂首字，与视频卡片保持一致 */
const initial = computed(() => collection.value?.title.trim().charAt(0) || '窗')

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  collection.value = null

  try {
    const detail = await getCollection(collectionId.value)
    collection.value = detail
    // 浏览器标题带上合集名，标签页 / 收藏夹里才分得清
    document.title = `${detail.title} · 窗月`
  } catch (e) {
    error.value = toErrorMessage(e)
  } finally {
    loading.value = false
  }
}

// 从一侧栏合集切到另一侧栏合集时路由复用同一组件，靠监听 id 重新拉取
watch(collectionId, () => void load(), { immediate: true })
</script>

<template>
  <div class="collection-detail cy-container">
    <div v-if="loading">
      <div class="collection-detail__skeleton">
        <div class="collection-detail__sk-cover cy-skeleton" />
        <div class="collection-detail__sk-info">
          <div class="collection-detail__sk-title cy-skeleton" />
          <div class="collection-detail__sk-line cy-skeleton" />
          <div class="collection-detail__sk-line collection-detail__sk-line--short cy-skeleton" />
        </div>
      </div>

      <div class="cy-grid collection-detail__grid">
        <VideoCardSkeleton v-for="index in 8" :key="index" />
      </div>
    </div>

    <StatePanel
      v-else-if="error"
      variant="error"
      title="合集加载失败"
      :description="error"
      action-label="重新加载"
      @action="load"
    />

    <template v-else-if="collection">
      <header class="collection-detail__head">
        <div class="collection-detail__cover">
          <img
            v-if="collection.cover_url"
            class="collection-detail__img"
            :src="collection.cover_url"
            :alt="collection.title"
          />
          <span v-else class="collection-detail__fallback" aria-hidden="true">{{ initial }}</span>
        </div>

        <div class="collection-detail__info">
          <p class="cy-caption collection-detail__eyebrow">合集</p>
          <h1 class="cy-title collection-detail__title">{{ collection.title }}</h1>

          <p class="cy-caption collection-detail__meta">
            共 <span class="cy-num">{{ collection.video_count }}</span> 个视频
            <template v-if="collection.created_at">
              · 创建于 {{ formatDate(collection.created_at) }}
            </template>
          </p>

          <p v-if="collection.description" class="collection-detail__desc">
            {{ collection.description }}
          </p>
        </div>
      </header>

      <StatePanel
        v-if="collection.videos.length === 0"
        title="这个合集还是空的"
        description="管理员正在往里面添加视频，稍后再来看看。"
      />

      <div v-else class="cy-grid collection-detail__grid">
        <VideoCard v-for="item in collection.videos" :key="item.id" :video="item" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.collection-detail {
  padding-top: var(--cy-space-5);
  padding-bottom: var(--cy-space-7);
}

/* 头部：左封面右信息，窄屏改为上下排 */
.collection-detail__head {
  display: flex;
  align-items: flex-start;
  gap: var(--cy-space-5);
  padding-bottom: var(--cy-space-6);
  border-bottom: 1px solid var(--cy-border);
}

.collection-detail__cover {
  flex-shrink: 0;
  width: 320px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-lg);
  overflow: hidden;
  background: var(--cy-surface-2);
}

.collection-detail__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.collection-detail__fallback {
  display: grid;
  place-items: center;
  width: 100%;
  height: 100%;
  background: var(--cy-primary);
  color: var(--cy-on-primary);
  font-size: 48px;
  font-weight: 600;
  letter-spacing: 2px;
}

.collection-detail__info {
  min-width: 0;
  padding-top: var(--cy-space-2);
}

.collection-detail__eyebrow {
  letter-spacing: 2px;
}

.collection-detail__title {
  margin: var(--cy-space-1) 0 var(--cy-space-2);
}

.collection-detail__meta {
  margin-bottom: var(--cy-space-3);
}

.collection-detail__desc {
  color: var(--cy-text-secondary);
  font-size: 14px;
  line-height: 22px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.collection-detail__grid {
  margin-top: var(--cy-space-6);
}

/* 骨架：与真实头部同尺寸，避免加载完成后页面高度突跳 */
.collection-detail__skeleton {
  display: flex;
  align-items: flex-start;
  gap: var(--cy-space-5);
}

.collection-detail__sk-cover {
  flex-shrink: 0;
  width: 320px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-lg);
}

.collection-detail__sk-info {
  flex: 1;
  min-width: 0;
  padding-top: var(--cy-space-2);
}

.collection-detail__sk-title {
  height: 28px;
  width: 50%;
  border-radius: var(--cy-radius-sm);
}

.collection-detail__sk-line {
  height: 14px;
  width: 40%;
  margin-top: var(--cy-space-3);
  border-radius: var(--cy-radius-sm);
}

.collection-detail__sk-line--short {
  width: 24%;
}

@media (max-width: 899px) {
  .collection-detail__head,
  .collection-detail__skeleton {
    flex-direction: column;
  }

  .collection-detail__cover,
  .collection-detail__sk-cover {
    width: 100%;
    max-width: 480px;
  }
}
</style>
