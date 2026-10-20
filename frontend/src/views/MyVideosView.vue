<script setup lang="ts">
// 「我」的四个列表页：赞过的视频 / 我的收藏 / 稍后再看 / 观看历史。
//
// 四个路由共用同一个组件，靠路由名决定数据源、标题与空态文案，
// 避免为结构完全一致的列表写四份。列表项复用首页的 VideoCard，
// 翻页方式与首页一致（不断追加），窄屏下页码不会溢出。

import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { listMyVideos, type MyVideoKind } from '@/api/interaction'
import { toErrorMessage } from '@/api/request'
import StatePanel from '@/components/StatePanel.vue'
import VideoCard from '@/components/VideoCard.vue'
import VideoCardSkeleton from '@/components/VideoCardSkeleton.vue'
import { usePlayerQueueStore } from '@/stores/playerQueue'
import type { Video } from '@/types/api'

const PAGE_SIZE = 20

interface ViewMeta {
  kind: MyVideoKind
  title: string
  emptyTitle: string
  emptyDescription: string
  /** 是否提供「一键加入播放队列」：仅收藏与稍后再看这两个「攒片单」性质的列表需要 */
  canQueue?: boolean
}

const META: Record<string, ViewMeta> = {
  'my-likes': {
    kind: 'likes',
    title: '赞过的视频',
    emptyTitle: '还没有赞过的视频',
    emptyDescription: '在观看页点个赞，喜欢的内容会出现在这里。',
  },
  'my-favorites': {
    kind: 'favorites',
    title: '我的收藏',
    emptyTitle: '收藏夹还是空的',
    emptyDescription: '在观看页点击收藏，稍后想看的视频都会留在这里。',
    canQueue: true,
  },
  'my-watch-later': {
    kind: 'watch-later',
    title: '稍后再看',
    emptyTitle: '稍后再看列表还是空的',
    emptyDescription: '在观看页点「稍后再看」，想留到之后看的视频都会存到这里。',
    canQueue: true,
  },
  'my-history': {
    kind: 'history',
    title: '观看历史',
    emptyTitle: '还没有观看记录',
    emptyDescription: '看过的视频会按最近观看时间出现在这里。',
  },
}

const route = useRoute()
const queueStore = usePlayerQueueStore()

const videos = ref<Video[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(true)
const loadingMore = ref(false)
const error = ref('')
const loadMoreError = ref('')

const meta = computed<ViewMeta>(() => META[String(route.name)] ?? META['my-history'])
const hasMore = computed(() => videos.value.length < total.value)

/** 「一键加入队列」的反馈文案，加入成功后短暂提示一次 */
const queueNotice = ref('')

/** 把当前已加载的内容加入播放队列，按 id 去重，提示实际新增的条数 */
function addAllToQueue(): void {
  const added = queueStore.enqueue(videos.value)
  queueNotice.value =
    added > 0 ? `已加入播放队列（新增 ${added} 条）` : '这些内容都已在播放队列中'
}

async function load(reset: boolean): Promise<void> {
  if (reset) {
    loading.value = true
    error.value = ''
    loadMoreError.value = ''
    queueNotice.value = ''
  } else {
    loadingMore.value = true
    loadMoreError.value = ''
  }

  const targetPage = reset ? 1 : page.value + 1

  try {
    const result = await listMyVideos(meta.value.kind, {
      page: targetPage,
      page_size: PAGE_SIZE,
    })
    const list = result.list ?? []
    // 按 id 去重：翻页期间内容有变化时可能重复返回同一条
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
// 三个列表共用组件，切换路由名时重新拉取对应数据源
watch(
  () => route.name,
  () => void load(true),
)
</script>

<template>
  <div class="mine cy-container">
    <div class="mine__head">
      <h1 class="mine__title cy-display">{{ meta.title }}</h1>
      <button
        v-if="meta.canQueue && videos.length > 0"
        class="cy-btn cy-btn--secondary cy-btn--pill"
        type="button"
        @click="addAllToQueue"
      >
        加入播放队列
      </button>
    </div>

    <p v-if="queueNotice" class="cy-caption mine__notice">{{ queueNotice }}</p>

    <div v-if="loading" class="cy-grid">
      <VideoCardSkeleton v-for="index in 8" :key="index" />
    </div>

    <StatePanel
      v-else-if="error"
      variant="error"
      title="内容加载失败"
      :description="error"
      action-label="重新加载"
      @action="load(true)"
    />

    <StatePanel
      v-else-if="videos.length === 0"
      :title="meta.emptyTitle"
      :description="meta.emptyDescription"
    />

    <template v-else>
      <div class="cy-grid">
        <VideoCard v-for="item in videos" :key="item.id" :video="item" />
      </div>

      <div class="mine__more">
        <div v-if="loadMoreError" class="cy-alert cy-alert--error mine__more-alert">
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
.mine {
  padding-top: var(--cy-space-5);
  padding-bottom: var(--cy-space-7);
}

.mine__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--cy-space-4);
  margin-bottom: var(--cy-space-5);
}

.mine__title {
  margin-bottom: 0;
}

.mine__notice {
  margin-bottom: var(--cy-space-4);
}

.mine__more {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-6);
}

.mine__more-alert {
  max-width: 460px;
}
</style>
