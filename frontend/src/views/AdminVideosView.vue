<script setup lang="ts">
// 管理后台 /admin/videos：内容池总览 + 上下架。
//
// 与首页的区别：这里调 GET /admin/videos，能看到全部状态（含已下架）的视频，
// 并在每条上提供「下架 / 重新上架」，对应契约里的 PATCH /admin/videos/{id}。
//
// 视觉沿用列表页的克制风格：表格用分割线而非卡片墙，只有状态标签带语义色，
// 其余一律走中性面（--cy-surface / --cy-border）。
//
// 这是管理员页面：路由 meta.requiresAdmin 保证只有管理员进得来，
// 入口在侧边导航「管理」分组。

import { computed, onMounted, ref } from 'vue'

import { toErrorMessage } from '@/api/request'
import {
  deleteVideo,
  listAdminVideos,
  retryTranscode,
  updateVideo,
  updateVideoStatus,
  type UpdateVideoPayload,
} from '@/api/video'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import StatePanel from '@/components/StatePanel.vue'
import VideoEditDialog from '@/components/VideoEditDialog.vue'
import {
  VIDEO_CATEGORIES,
  VIDEO_STATUS_FAILED,
  VIDEO_STATUS_OFFLINE,
  VIDEO_STATUS_PUBLISHED,
  VIDEO_STATUS_TRANSCODING,
  type Video,
} from '@/types/api'
import { formatCount } from '@/utils/format'

const PAGE_SIZE = 20

interface StatusFilter {
  label: string
  /** 不传表示不过滤 */
  value?: number
}

const statusFilters: StatusFilter[] = [
  { label: '全部' },
  { label: '转码中', value: VIDEO_STATUS_TRANSCODING },
  { label: '已上架', value: VIDEO_STATUS_PUBLISHED },
  { label: '已下架', value: VIDEO_STATUS_OFFLINE },
  { label: '转码失败', value: VIDEO_STATUS_FAILED },
]

const videos = ref<Video[]>([])
const total = ref(0)
const page = ref(1)
const statusFilter = ref<number | undefined>(undefined)
/** 输入框里的关键词，回车 / 点「搜索」才生效 */
const keywordInput = ref('')
const keyword = ref('')

const loading = ref(true)
const error = ref('')
/** 上下架失败时的行内提示，不覆盖整页 */
const actionError = ref('')
/** 正在上下架的视频 id，用于禁用按钮并给出「处理中」反馈 */
const pendingId = ref('')

/** 编辑弹框 */
const editOpen = ref(false)
const editing = ref<Video | null>(null)
const editSaving = ref(false)
const editError = ref('')

/** 删除确认弹框 */
const deleteOpen = ref(false)
const deleting = ref<Video | null>(null)
const deleteSaving = ref(false)
const deleteError = ref('')

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

/** 分类名称：后端只存数字，名称与上传页共用同一份前端定义 */
function categoryName(id: number): string {
  return VIDEO_CATEGORIES.find((item) => item.id === id)?.name ?? '未分类'
}

function statusText(status: number): string {
  if (status === VIDEO_STATUS_PUBLISHED) {
    return '已上架'
  }
  if (status === VIDEO_STATUS_OFFLINE) {
    return '已下架'
  }
  if (status === VIDEO_STATUS_FAILED) {
    return '转码失败'
  }
  return '转码中'
}

function statusClass(status: number): string {
  if (status === VIDEO_STATUS_PUBLISHED) {
    return 'is-published'
  }
  if (status === VIDEO_STATUS_OFFLINE) {
    return 'is-offline'
  }
  if (status === VIDEO_STATUS_FAILED) {
    return 'is-failed'
  }
  return 'is-transcoding'
}

/** 只有上架 / 下架两种状态可以手动切换；转码中与转码失败不提供上下架入口 */
function canToggleStatus(status: number): boolean {
  return status === VIDEO_STATUS_PUBLISHED || status === VIDEO_STATUS_OFFLINE
}

/** 表格里的时间用绝对时间，相对时间在管理场景里不够精确 */
function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return '—'
  }
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`
}

async function load(targetPage: number): Promise<void> {
  loading.value = true
  error.value = ''
  actionError.value = ''

  try {
    const result = await listAdminVideos({
      page: targetPage,
      page_size: PAGE_SIZE,
      status: statusFilter.value,
      keyword: keyword.value || undefined,
    })
    videos.value = result.list ?? []
    total.value = result.total ?? 0
    page.value = result.page ?? targetPage
  } catch (e) {
    error.value = toErrorMessage(e)
    videos.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function selectStatus(value: number | undefined): void {
  if (value === statusFilter.value) {
    return
  }
  statusFilter.value = value
  void load(1)
}

function submitSearch(): void {
  keyword.value = keywordInput.value.trim()
  void load(1)
}

/**
 * 把一行更新写回列表。
 *
 * 若当前处于某个状态筛选下、而新状态不再符合条件（例如在「转码失败」里点了重新转码，
 * 状态变成「转码中」），就地移除并修正总数，避免出现「筛失败却显示转码中」的错位。
 */
function applyRowUpdate(updated: Video): void {
  const index = videos.value.findIndex((item) => item.id === updated.id)
  if (index < 0) {
    return
  }
  if (statusFilter.value !== undefined && statusFilter.value !== updated.status) {
    videos.value.splice(index, 1)
    total.value = Math.max(0, total.value - 1)
  } else {
    videos.value[index] = updated
  }
}

/**
 * 切换上架状态。
 *
 * 若当前处于「已上架 / 已下架」筛选下，切换后该行不再符合筛选条件，
 * 就地移除并修正总数，避免出现「筛已下架却还显示已上架」的错位。
 */
async function toggleStatus(video: Video): Promise<void> {
  if (pendingId.value) {
    return
  }
  const next =
    video.status === VIDEO_STATUS_PUBLISHED ? VIDEO_STATUS_OFFLINE : VIDEO_STATUS_PUBLISHED

  pendingId.value = video.id
  actionError.value = ''
  try {
    const updated = await updateVideoStatus(video.id, next)
    applyRowUpdate(updated)
  } catch (e) {
    actionError.value = toErrorMessage(e)
  } finally {
    pendingId.value = ''
  }
}

/**
 * 重新转码：只对「转码失败」的行出现。
 *
 * 复用已上传的源文件，不会重新上传；成功后该行状态变回「转码中」，
 * 若当前正筛选「转码失败」，它会按筛选条件被移除。
 */
async function retry(video: Video): Promise<void> {
  if (pendingId.value) {
    return
  }
  pendingId.value = video.id
  actionError.value = ''
  try {
    const updated = await retryTranscode(video.id)
    applyRowUpdate(updated)
  } catch (e) {
    actionError.value = toErrorMessage(e)
  } finally {
    pendingId.value = ''
  }
}

function openEdit(video: Video): void {
  editing.value = video
  editError.value = ''
  editOpen.value = true
}

function closeEdit(): void {
  editOpen.value = false
  editing.value = null
  editError.value = ''
}

/** 保存编辑：成功后就地替换该行，不整页重拉（筛选条件不变，行状态也不变） */
async function submitEdit(payload: UpdateVideoPayload): Promise<void> {
  if (!editing.value || editSaving.value) {
    return
  }
  editSaving.value = true
  editError.value = ''
  try {
    const updated = await updateVideo(editing.value.id, payload)
    const index = videos.value.findIndex((item) => item.id === updated.id)
    if (index >= 0) {
      videos.value[index] = updated
    }
    closeEdit()
  } catch (e) {
    // 失败时保留弹框，让用户能改完重试，输入不丢
    editError.value = toErrorMessage(e)
  } finally {
    editSaving.value = false
  }
}

function askDelete(video: Video): void {
  deleting.value = video
  deleteError.value = ''
  deleteOpen.value = true
}

function closeDelete(): void {
  deleteOpen.value = false
  deleting.value = null
  deleteError.value = ''
}

/**
 * 确认删除：不可恢复，成功后该行消失、总数减一。
 * 若当前页因此被删空且不在第一页，回退一页，避免停在空页上。
 */
async function confirmDelete(): Promise<void> {
  if (!deleting.value || deleteSaving.value) {
    return
  }
  const target = deleting.value
  deleteSaving.value = true
  deleteError.value = ''
  try {
    await deleteVideo(target.id)
    videos.value = videos.value.filter((item) => item.id !== target.id)
    total.value = Math.max(0, total.value - 1)
    closeDelete()

    if (videos.value.length === 0 && page.value > 1) {
      await load(page.value - 1)
    }
  } catch (e) {
    deleteError.value = toErrorMessage(e)
  } finally {
    deleteSaving.value = false
  }
}

onMounted(() => void load(1))
</script>

<template>
  <div class="admin cy-container">
    <header class="admin__head">
      <h1 class="cy-title">视频管理</h1>
      <p v-if="!loading && !error" class="cy-caption">
        共 <span class="cy-num">{{ total }}</span> 条内容
      </p>
    </header>

    <div class="admin__filters" role="group" aria-label="状态筛选">
      <button
        v-for="filter in statusFilters"
        :key="filter.label"
        class="cy-btn cy-btn--secondary cy-btn--pill cy-btn--sm"
        :class="{ 'is-active': statusFilter === filter.value }"
        type="button"
        :aria-pressed="statusFilter === filter.value"
        @click="selectStatus(filter.value)"
      >
        {{ filter.label }}
      </button>
    </div>

    <form class="admin__search" role="search" @submit.prevent="submitSearch">
      <input
        v-model="keywordInput"
        class="cy-input admin__search-input"
        type="search"
        placeholder="搜索标题 / 简介"
        aria-label="搜索标题或简介"
        @keydown.enter.prevent="submitSearch"
      />
      <button class="cy-btn cy-btn--primary cy-btn--sm" type="submit">搜索</button>
    </form>

    <p v-if="actionError" class="cy-alert cy-alert--error admin__alert">{{ actionError }}</p>

    <!-- 加载中：占位行与真实行同高，避免加载完成后表格高度突跳 -->
    <div v-if="loading" class="admin__table" aria-hidden="true">
      <div v-for="index in 6" :key="index" class="admin__row admin__row--skeleton">
        <span class="cy-skeleton admin__sk-cover" />
        <span class="cy-skeleton admin__sk-line" />
        <span class="cy-skeleton admin__sk-line admin__sk-line--short" />
      </div>
    </div>

    <StatePanel
      v-else-if="error"
      variant="error"
      title="内容加载失败"
      :description="error"
      action-label="重新加载"
      @action="load(page)"
    />

    <StatePanel
      v-else-if="videos.length === 0"
      title="这里还没有内容"
      :description="
        keyword
          ? '没有匹配的内容，换个关键词试试。'
          : statusFilter === undefined
            ? '还没有任何投稿，去「投稿」页发布第一条内容吧。'
            : '当前状态下没有内容，换个筛选看看。'
      "
    />

    <template v-else>
      <div class="admin__table">
        <div class="admin__row admin__row--head">
          <span>内容</span>
          <span class="admin__cell--cat">分类</span>
          <span>状态</span>
          <span class="admin__cell--num">播放量</span>
          <span class="admin__cell--time">发布时间</span>
          <span class="admin__cell--action">操作</span>
        </div>

        <div v-for="video in videos" :key="video.id" class="admin__row">
          <div class="admin__main">
            <div class="admin__cover">
              <img v-if="video.cover_url" :src="video.cover_url" :alt="video.title" loading="lazy" />
              <span v-else class="admin__cover-empty" aria-hidden="true">
                {{ video.title.trim().charAt(0) || '窗' }}
              </span>
            </div>
            <div class="admin__info">
              <RouterLink
                class="admin__title cy-clamp-2"
                :to="{ name: 'video-detail', params: { id: video.id } }"
              >
                {{ video.title }}
              </RouterLink>
              <p v-if="video.description" class="admin__desc cy-clamp-1 cy-caption">
                {{ video.description }}
              </p>
              <p
                v-if="video.status === VIDEO_STATUS_FAILED && video.error_msg"
                class="admin__error cy-clamp-1 cy-caption"
              >
                转码失败：{{ video.error_msg }}
              </p>
            </div>
          </div>

          <span class="admin__cell--cat cy-caption">{{ categoryName(video.category_id) }}</span>

          <span class="admin__status" :class="statusClass(video.status)">
            {{ statusText(video.status) }}
          </span>

          <span class="admin__cell--num cy-num">{{ formatCount(video.view_count) }}</span>

          <span class="admin__cell--time cy-caption cy-num">{{ formatDate(video.created_at) }}</span>

          <span class="admin__cell--action admin__actions">
            <button
              v-if="video.status === VIDEO_STATUS_FAILED"
              class="cy-btn cy-btn--outline cy-btn--sm"
              type="button"
              :disabled="pendingId === video.id"
              @click="retry(video)"
            >
              {{ pendingId === video.id ? '提交中…' : '重新转码' }}
            </button>
            <button
              class="cy-btn cy-btn--outline cy-btn--sm"
              type="button"
              :disabled="pendingId === video.id"
              @click="openEdit(video)"
            >
              编辑
            </button>
            <button
              v-if="canToggleStatus(video.status)"
              class="cy-btn cy-btn--outline cy-btn--sm"
              type="button"
              :disabled="pendingId === video.id"
              @click="toggleStatus(video)"
            >
              {{
                pendingId === video.id
                  ? '处理中…'
                  : video.status === VIDEO_STATUS_PUBLISHED
                    ? '下架'
                    : '重新上架'
              }}
            </button>
            <button
              class="cy-btn cy-btn--outline cy-btn--sm admin__danger"
              type="button"
              @click="askDelete(video)"
            >
              删除
            </button>
          </span>
        </div>
      </div>

      <div v-if="totalPages > 1" class="admin__pager">
        <button
          class="cy-btn cy-btn--secondary cy-btn--sm cy-btn--pill"
          type="button"
          :disabled="page <= 1"
          @click="load(page - 1)"
        >
          上一页
        </button>
        <span class="cy-caption cy-num">第 {{ page }} / {{ totalPages }} 页</span>
        <button
          class="cy-btn cy-btn--secondary cy-btn--sm cy-btn--pill"
          type="button"
          :disabled="page >= totalPages"
          @click="load(page + 1)"
        >
          下一页
        </button>
      </div>
    </template>

    <VideoEditDialog
      :open="editOpen"
      :video="editing"
      :saving="editSaving"
      :error="editError"
      @close="closeEdit"
      @submit="submitEdit"
    />

    <ConfirmDialog
      :open="deleteOpen"
      title="删除这条视频？"
      :description="`「${deleting?.title ?? ''}」将被永久删除，视频文件与封面会一并清理，且无法恢复。`"
      confirm-label="删除"
      danger
      :busy="deleteSaving"
      :error="deleteError"
      @close="closeDelete"
      @confirm="confirmDelete"
    />
  </div>
</template>

<style scoped>
.admin {
  padding-bottom: var(--cy-space-7);
}

.admin__head {
  display: flex;
  align-items: baseline;
  gap: var(--cy-space-3);
  padding: var(--cy-space-5) 0 var(--cy-space-4);
}

.admin__filters {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  padding-bottom: var(--cy-space-4);
  overflow-x: auto;
  scrollbar-width: none;
}

.admin__filters::-webkit-scrollbar {
  display: none;
}

.admin__filters > * {
  flex-shrink: 0;
}

/* 搜索行：输入框占满剩余宽度，按钮不参与收缩 */
.admin__search {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  padding-bottom: var(--cy-space-4);
}

.admin__search-input {
  flex: 1;
  min-width: 0;
  max-width: 420px;
}

.admin__alert {
  margin-bottom: var(--cy-space-4);
}

/* 表格：六列网格，表头与数据行共用同一套列宽 */
.admin__table {
  border-top: 1px solid var(--cy-border);
}

.admin__row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 84px 88px 96px 140px 232px;
  align-items: center;
  gap: var(--cy-space-4);
  padding: var(--cy-space-3) 0;
  border-bottom: 1px solid var(--cy-border);
}

.admin__row--head {
  padding: var(--cy-space-2) 0;
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
  font-weight: 600;
}

.admin__main {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  min-width: 0;
}

.admin__cover {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 96px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-sm);
  overflow: hidden;
  background: var(--cy-surface-2);
}

.admin__cover img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

/* 无封面：星紫纯色平涂 + 月白首字，与首页卡片保持一致 */
.admin__cover-empty {
  width: 100%;
  height: 100%;
  display: grid;
  place-items: center;
  background: var(--cy-primary);
  color: var(--cy-on-primary);
  font-size: 20px;
  font-weight: 600;
}

.admin__info {
  min-width: 0;
}

.admin__title {
  display: block;
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
  font-weight: 500;
}

.admin__title:hover {
  color: var(--cy-accent);
}

.admin__desc {
  margin-top: 2px;
}

/* 转码失败原因：只在失败行出现，用危险色文字提示，不铺底色 */
.admin__error {
  margin-top: 2px;
  color: var(--cy-danger);
}

.admin__cell--cat,
.admin__cell--num,
.admin__cell--time {
  color: var(--cy-text-secondary);
}

.admin__cell--num,
.admin__cell--time {
  font-size: 13px;
}

.admin__cell--action {
  min-width: 0;
}

/* 操作列：三个按钮左对齐排成一行 */
.admin__actions {
  display: flex;
  justify-content: flex-start;
  gap: var(--cy-space-2);
}

/* 删除是低频且有破坏性的操作：基座与相邻操作完全一致，只把文字与描边换成危险色，
   避免整张表都是红色 */
.admin__danger {
  color: var(--cy-danger);
}

.admin__actions .admin__danger:hover:not(:disabled) {
  border-color: var(--cy-danger);
  color: var(--cy-danger);
}

/* 状态标签：只有这里带语义色，其余一律中性 */
.admin__status {
  justify-self: start;
  height: 24px;
  padding: 0 10px;
  border-radius: var(--cy-radius-full);
  font-size: 12px;
  line-height: 24px;
  white-space: nowrap;
}

.admin__status.is-published {
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

.admin__status.is-offline {
  background: var(--cy-surface-2);
  color: var(--cy-text-muted);
}

.admin__status.is-transcoding {
  background: var(--cy-surface-2);
  color: var(--cy-warning);
}

.admin__status.is-failed {
  background: var(--cy-danger-soft);
  color: var(--cy-danger);
}

/* 骨架行：与真实行同高（封面 54px + 上下内边距） */
.admin__row--skeleton {
  grid-template-columns: 96px 1fr 60px;
}

.admin__sk-cover {
  width: 96px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-sm);
}

.admin__sk-line {
  height: 14px;
  border-radius: var(--cy-radius-xs);
}

.admin__sk-line--short {
  width: 60%;
}

.admin__pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--cy-space-4);
  margin-top: var(--cy-space-5);
}

/* 窄屏：隐藏次要列，表格降级成「封面 + 标题 + 状态 + 操作」 */
@media (max-width: 899px) {
  .admin__row {
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: var(--cy-space-3);
  }

  .admin__row--head,
  .admin__cell--cat,
  .admin__cell--num,
  .admin__cell--time {
    display: none;
  }
}
</style>
