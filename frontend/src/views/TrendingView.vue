<script setup lang="ts">
// 热门：按热度排序的视频榜单。
//
// 与首页共用 /videos 列表接口，只是把 sort 传成 hot，排序由服务端完成；
// 卡片网格、骨架屏、加载更多的交互与首页保持一致。

import { computed, onMounted, ref } from 'vue'

import { listVideos } from '@/api/video'
import { toErrorMessage } from '@/api/request'
import StatePanel from '@/components/StatePanel.vue'
import VideoCard from '@/components/VideoCard.vue'
import VideoCardSkeleton from '@/components/VideoCardSkeleton.vue'
import { useUserStore } from '@/stores/user'
import type { Video } from '@/types/api'

const PAGE_SIZE = 20

const userStore = useUserStore()

const videos = ref<Video[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(true)
const loadingMore = ref(false)
const error = ref('')
const loadMoreError = ref('')

const hasMore = computed(() => videos.value.length < total.value)

const emptyDescription = computed(() =>
  userStore.isAdmin
    ? '内容还没有发布，稍后就会有新的窗口亮起。'
    : '窗月正在准备第一批影片，稍后再来看看。',
)

async function load(reset: boolean): Promise<void> {
  if (reset) {
    loading.value = true
    error.value = ''
    loadMoreError.value = ''
  } else {
    loadingMore.value = true
    loadMoreError.value = ''
  }

  const targetPage = reset ? 1 : page.value + 1

  try {
    const result = await listVideos({
      page: targetPage,
      page_size: PAGE_SIZE,
      sort: 'hot',
    })
    const list = result.list ?? []
    // 按 id 去重：翻页期间热度变化可能让同一条视频重复出现
    const merged = reset ? list : [...videos.value, ...list]
    videos.value = [...new Map(merged.map((item) => [item.id, item])).values()]
    total.value = result.total ?? 0
    page.value = result.page ?? targetPage
  } catch (e) {
    if (reset) {
      error.value = toErrorMessage(e)
      videos.value = []
      total.value = 0
    } else {
      loadMoreError.value = toErrorMessage(e)
    }
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

onMounted(() => void load(true))
</script>

<template>
  <div class="trending cy-container">
    <h1 class="cy-title trending__title">热门</h1>

    <div v-if="loading" class="cy-grid">
      <VideoCardSkeleton v-for="index in PAGE_SIZE" :key="index" />
    </div>

    <StatePanel
      v-else-if="error"
      variant="error"
      title="榜单加载失败"
      :description="error"
      action-label="重新加载"
      @action="load(true)"
    />

    <StatePanel
      v-else-if="videos.length === 0"
      title="榜单还空着"
      :description="emptyDescription"
    />

    <template v-else>
      <div class="cy-grid">
        <VideoCard v-for="item in videos" :key="item.id" :video="item" />
      </div>

      <div class="trending__more">
        <div v-if="loadMoreError" class="cy-alert cy-alert--error trending__more-alert">
          {{ loadMoreError }}
        </div>

        <button
          v-if="hasMore"
          class="cy-btn cy-btn--secondary cy-btn--pill"
          type="button"
          :disabled="loadingMore"
          @click="load(false)"
        >
          {{ loadingMore ? '加载中…' : '加载更多' }}
        </button>
        <p v-else class="cy-caption">已经看到全部内容</p>
      </div>
    </template>
  </div>
</template>

<style scoped>
.trending {
  padding-bottom: var(--cy-space-7);
}

.trending__title {
  margin: var(--cy-space-4) 0 var(--cy-space-5);
}

.trending__more {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-6);
}

.trending__more-alert {
  max-width: 460px;
}
</style>
