<script setup lang="ts">
// 首页：推荐流 + 分类筛选 + 视频网格 + 加载更多。
//
// 三种数据来源各司其职：默认浏览态走推荐流（GET /feed）；只选分类时走视频列表的分类浏览
// （GET /videos）；带关键词时走独立搜索服务（GET /search/videos，可叠加分类筛选）。
// 翻页方式为「加载更多」而非页码分页：窄屏下页码会溢出，且不断追加更符合浏览习惯。

import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { getHomeFeed } from '@/api/feed'
import { toErrorMessage } from '@/api/request'
import { searchVideos } from '@/api/search'
import { listVideos } from '@/api/video'
import CategoryChips from '@/components/CategoryChips.vue'
import StatePanel from '@/components/StatePanel.vue'
import VideoCard from '@/components/VideoCard.vue'
import VideoCardSkeleton from '@/components/VideoCardSkeleton.vue'
import { useUserStore } from '@/stores/user'
import { VIDEO_CATEGORIES, type PageResult, type VideoCardItem } from '@/types/api'

const PAGE_SIZE = 20

const userStore = useUserStore()
const route = useRoute()
const router = useRouter()

const videos = ref<VideoCardItem[]>([])
const total = ref(0)
const page = ref(1)
const categoryId = ref(0)
const loading = ref(true)
const loadingMore = ref(false)
const error = ref('')
const loadMoreError = ref('')

const hasMore = computed(() => videos.value.length < total.value)

// 关键词来自顶栏搜索（?q=），空串表示未搜索
const keyword = computed(() => (typeof route.query.q === 'string' ? route.query.q.trim() : ''))

// 空状态按角色区分：管理员看到的是「内容池还空着」，普通观众看到的是「稍后再来」
const emptyDescription = computed(() =>
  userStore.isAdmin
    ? '内容还没有发布，稍后就会有新的窗口亮起。'
    : '窗月正在准备第一批影片，换个分类看看，或稍后再来。',
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
    // 三种来源各司其职：有关键词走搜索服务（可叠加分类），只选分类走视频列表的分类浏览，
    // 都没有则走首页推荐流。
    let result: PageResult<VideoCardItem>
    if (keyword.value) {
      result = await searchVideos({
        keyword: keyword.value,
        category_id: categoryId.value || undefined,
        page: targetPage,
        page_size: PAGE_SIZE,
      })
    } else if (categoryId.value) {
      result = await listVideos({
        page: targetPage,
        page_size: PAGE_SIZE,
        category_id: categoryId.value,
      })
    } else {
      result = await getHomeFeed({ page: targetPage, page_size: PAGE_SIZE })
    }
    const list = result.list ?? []
    // 按 id 去重：翻页期间若有新视频上架，可能重复返回同一条
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

function selectCategory(next: number): void {
  if (next === categoryId.value) {
    return
  }
  categoryId.value = next
  void load(true)
}

/** 内容池空着时，管理员从这里直达投稿页 */
function goUpload(): void {
  void router.push({ name: 'admin-upload' })
}

// 顶栏搜索改的是 ?q=，关键词变化时回到第一页重新查询
watch(keyword, () => void load(true))

onMounted(() => void load(true))
</script>

<template>
  <div class="home cy-container">
    <!-- 筛选行吸顶：滚轮下滑时与左侧导航一样始终可见 -->
    <div class="home__filter">
      <CategoryChips
        :model-value="categoryId"
        :categories="VIDEO_CATEGORIES"
        @update:model-value="selectCategory"
      />
    </div>

    <!-- 搜索态：显示结果标题；无结果时给出空态 -->
    <template v-if="keyword">
      <h1 class="home__search-title">
        「{{ keyword }}」的搜索结果
        <span v-if="!loading && !error" class="home__search-count">共 {{ total }} 个</span>
      </h1>

      <div v-if="loading" class="cy-grid">
        <VideoCardSkeleton v-for="index in PAGE_SIZE" :key="index" />
      </div>

      <StatePanel
        v-else-if="error"
        variant="error"
        title="搜索失败"
        :description="error"
        action-label="重新搜索"
        @action="load(true)"
      />

      <StatePanel
        v-else-if="videos.length === 0"
        title="没有找到相关视频"
        description="换个关键词试试，或点上方「全部」查看所有内容。"
      />

      <template v-else>
        <div class="cy-grid">
          <VideoCard v-for="item in videos" :key="item.id" :video="item" />
        </div>
      </template>
    </template>

    <!-- 浏览态：分类筛选下的常规列表 -->
    <template v-else>
      <!-- 加载中：骨架屏数量与每页真实条数一致，避免加载完成后页面突然变长 -->
      <div v-if="loading" class="cy-grid">
        <VideoCardSkeleton v-for="index in PAGE_SIZE" :key="index" />
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
        title="这里还没有视频"
        :description="emptyDescription"
        :action-label="userStore.isAdmin ? '上传视频' : ''"
        @action="goUpload"
      />

      <template v-else>
        <div class="cy-grid">
          <VideoCard v-for="item in videos" :key="item.id" :video="item" />
        </div>
      </template>
    </template>

    <div v-if="!loading && !error && videos.length > 0" class="home__more">
      <div v-if="loadMoreError" class="cy-alert cy-alert--error home__more-alert">
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
  </div>
</template>

<style scoped>
.home {
  padding-bottom: var(--cy-space-7);
}

/*
 * 吸顶容器：sticky 顶到顶栏下沿。
 * 负外边距抵消 .cy-container 的左右留白，再用内边距补回来，
 * 这样滚动时内容不会从筛选行两侧透出。
 */
.home__filter {
  position: sticky;
  top: var(--cy-header-h);
  z-index: 50;
  margin: 0 calc(-1 * var(--cy-page-pad));
  padding: var(--cy-space-4) var(--cy-page-pad) var(--cy-space-3);
  background: var(--cy-bg);
}

.home__more {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-6);
}

.home__search-title {
  display: flex;
  align-items: baseline;
  gap: var(--cy-space-3);
  margin: var(--cy-space-4) 0 var(--cy-space-5);
  font-size: 20px;
  line-height: 28px;
  font-weight: 600;
  color: var(--cy-text);
}

.home__search-count {
  color: var(--cy-text-muted);
  font-size: 13px;
  font-weight: 400;
}

.home__more-alert {
  max-width: 460px;
}
</style>
