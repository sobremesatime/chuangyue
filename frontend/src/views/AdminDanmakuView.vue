<script setup lang="ts">
// 管理后台 /admin/danmaku：弹幕治理。
//
// 跨视频汇总全部弹幕，可按内容关键词检索并对违规弹幕做软删处理。
// 时间轴位置与模式是弹幕特有的两个维度，展示时都转成可读文案。
//
// 视觉与管理后台其余页面一致：表格式排布、中性面为主，加载 / 空 / 错误三态齐全。
// 管理员页面，入口在侧边导航「管理」分组。

import { computed, onMounted, ref } from 'vue'

import { deleteAdminDanmaku, listAdminDanmaku } from '@/api/danmaku'
import { toErrorMessage } from '@/api/request'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import StatePanel from '@/components/StatePanel.vue'
import type { AdminDanmaku } from '@/types/api'

const PAGE_SIZE = 20

const danmaku = ref<AdminDanmaku[]>([])
const total = ref(0)
const page = ref(1)

/** 输入框草稿与已生效关键词分开，回车 / 点搜索才应用 */
const searchInput = ref('')
const keyword = ref('')

const loading = ref(true)
const error = ref('')

/** 删除确认弹框 */
const deleteOpen = ref(false)
const deleting = ref<AdminDanmaku | null>(null)
const deleteSaving = ref(false)
const deleteError = ref('')

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

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

/** 毫秒时间轴 -> mm:ss */
function formatTimeMs(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000))
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${pad(m)}:${pad(s)}`
}

/** 弹幕模式：1 滚动、2 顶部、3 底部 */
function modeText(mode: number): string {
  if (mode === 2) {
    return '顶部'
  }
  if (mode === 3) {
    return '底部'
  }
  return '滚动'
}

async function load(targetPage: number): Promise<void> {
  loading.value = true
  error.value = ''

  try {
    const result = await listAdminDanmaku({
      page: targetPage,
      page_size: PAGE_SIZE,
      keyword: keyword.value || undefined,
    })
    danmaku.value = result.list ?? []
    total.value = result.total ?? 0
    page.value = result.page ?? targetPage
  } catch (e) {
    error.value = toErrorMessage(e)
    danmaku.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

/** 应用搜索：有新关键词才回到第一页重查，避免空点造成重复请求 */
function submitSearch(): void {
  const next = searchInput.value.trim()
  if (next === keyword.value) {
    return
  }
  keyword.value = next
  void load(1)
}

/** 清空关键词，恢复全部 */
function clearSearch(): void {
  if (!keyword.value && !searchInput.value) {
    return
  }
  searchInput.value = ''
  keyword.value = ''
  void load(1)
}

function askDelete(item: AdminDanmaku): void {
  deleting.value = item
  deleteError.value = ''
  deleteOpen.value = true
}

function closeDelete(): void {
  deleteOpen.value = false
  deleting.value = null
  deleteError.value = ''
}

/**
 * 确认删除：软删成功后该行消失、总数减一。
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
    await deleteAdminDanmaku(target.id)
    danmaku.value = danmaku.value.filter((item) => item.id !== target.id)
    total.value = Math.max(0, total.value - 1)
    closeDelete()

    if (danmaku.value.length === 0 && page.value > 1) {
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
      <h1 class="cy-title">弹幕管理</h1>
      <p v-if="!loading && !error" class="cy-caption">
        共 <span class="cy-num">{{ total }}</span> 条弹幕
      </p>
    </header>

    <form class="admin__search" role="search" @submit.prevent="submitSearch">
      <input
        v-model="searchInput"
        class="cy-input admin__search-input"
        type="search"
        placeholder="搜索弹幕内容"
        aria-label="搜索弹幕内容"
      />
      <button v-if="keyword || searchInput" class="cy-btn cy-btn--outline" type="button" @click="clearSearch">
        清空
      </button>
      <button class="cy-btn cy-btn--primary" type="submit">搜索</button>
    </form>

    <!-- 加载中：占位行与真实行同高，避免加载完成后表格高度突跳 -->
    <div v-if="loading" class="admin__table" aria-hidden="true">
      <div v-for="index in 6" :key="index" class="admin__row admin__row--skeleton">
        <span class="cy-skeleton admin__sk-line" />
        <span class="cy-skeleton admin__sk-line admin__sk-line--short" />
      </div>
    </div>

    <StatePanel
      v-else-if="error"
      variant="error"
      title="弹幕加载失败"
      :description="error"
      action-label="重新加载"
      @action="load(page)"
    />

    <StatePanel
      v-else-if="danmaku.length === 0"
      title="没有找到弹幕"
      :description="keyword ? '换个关键词试试，或清空后再看全部。' : '当前还没有任何弹幕。'"
    />

    <template v-else>
      <div class="admin__table">
        <div class="admin__row admin__row--head">
          <span>弹幕内容</span>
          <span class="admin__cell--video">所属视频</span>
          <span class="admin__cell--author">发送者</span>
          <span class="admin__cell--pos">位置</span>
          <span class="admin__cell--mode">模式</span>
          <span class="admin__cell--time">时间</span>
          <span class="admin__cell--action">操作</span>
        </div>

        <div v-for="item in danmaku" :key="item.id" class="admin__row">
          <p class="admin__text cy-clamp-2">{{ item.content }}</p>

          <RouterLink
            v-if="item.video_id"
            class="admin__cell--video admin__link cy-clamp-2"
            :to="{ name: 'video-detail', params: { id: item.video_id } }"
          >
            {{ item.video_title }}
          </RouterLink>
          <span v-else class="admin__cell--video cy-caption">{{ item.video_title }}</span>

          <span class="admin__cell--author cy-clamp-1">{{ item.nickname }}</span>

          <span class="admin__cell--pos cy-num">{{ formatTimeMs(item.time_ms) }}</span>

          <span class="admin__cell--mode">{{ modeText(item.mode) }}</span>

          <span class="admin__cell--time cy-caption cy-num">{{ formatDate(item.created_at) }}</span>

          <span class="admin__cell--action admin__actions">
            <button
              class="cy-btn cy-btn--outline cy-btn--sm admin__danger"
              type="button"
              @click="askDelete(item)"
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

    <ConfirmDialog
      :open="deleteOpen"
      title="删除这条弹幕？"
      :description="`「${deleting?.content ?? ''}」将被删除，弹幕将不再出现在播放器上，且无法恢复。`"
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

.admin__search {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  padding-bottom: var(--cy-space-4);
}

.admin__search-input {
  max-width: 320px;
}

/* 表格：七列网格，表头与数据行共用同一套列宽 */
.admin__table {
  border-top: 1px solid var(--cy-border);
}

.admin__row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 160px 130px 64px 64px 140px 88px;
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

.admin__text {
  min-width: 0;
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
}

.admin__link {
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 18px;
}

.admin__link:hover {
  color: var(--cy-accent);
}

.admin__cell--author,
.admin__cell--pos,
.admin__cell--mode {
  min-width: 0;
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 18px;
}

.admin__cell--time {
  font-size: 13px;
  color: var(--cy-text-secondary);
}

.admin__cell--action {
  min-width: 0;
}

/* 操作列：按钮左对齐 */
.admin__actions {
  display: flex;
  justify-content: flex-start;
  gap: var(--cy-space-2);
}

/* 删除低频且有破坏性：基座与相邻操作一致，只把文字与描边换成危险色 */
.admin__danger {
  color: var(--cy-danger);
}

.admin__actions .admin__danger:hover:not(:disabled) {
  border-color: var(--cy-danger);
  color: var(--cy-danger);
}

/* 骨架行：与真实行同高 */
.admin__row--skeleton {
  grid-template-columns: 1fr 60px;
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

/* 窄屏：隐藏次要列，表格降级成「弹幕内容 + 操作」 */
@media (max-width: 899px) {
  .admin__row {
    grid-template-columns: minmax(0, 1fr) auto;
    gap: var(--cy-space-3);
  }

  .admin__row--head,
  .admin__cell--video,
  .admin__cell--author,
  .admin__cell--pos,
  .admin__cell--mode,
  .admin__cell--time {
    display: none;
  }
}
</style>