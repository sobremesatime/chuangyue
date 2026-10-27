<script setup lang="ts">
// 管理后台 /admin/collections：合集的增删改与「编排视频」。
//
// 页面分两种形态，靠 panelCollection 切换：
//   - 列表：标题 / 视频数 / 状态 / 创建时间 / 操作，分页与视频管理一致；
//   - 编排面板：左侧搜索或用 ID 添加视频，右侧是合集内视频的当前顺序，
//     上移 / 下移只改本地顺序，点「保存排序」才调用 PATCH …/order。
//
// 添加 / 移除是即时生效的（各自有独立接口），排序则需要显式保存；
// 这样误点上下移不会立刻写坏线上顺序，而增删又能马上看到结果。

import { computed, onMounted, ref } from 'vue'

import {
  addCollectionVideos,
  createCollection,
  deleteCollection,
  getAdminCollection,
  listAdminCollections,
  removeCollectionVideo,
  reorderCollectionVideos,
  updateCollection,
} from '@/api/collection'
import { toErrorMessage } from '@/api/request'
import { getVideo, listAdminVideos } from '@/api/video'
import CollectionEditDialog from '@/components/CollectionEditDialog.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import StatePanel from '@/components/StatePanel.vue'
import {
  COLLECTION_STATUS_DRAFT,
  COLLECTION_STATUS_PUBLISHED,
  VIDEO_CATEGORIES,
  type AdminCollection,
  type CreateCollectionPayload,
  type Video,
} from '@/types/api'
import { formatCount } from '@/utils/format'

const PAGE_SIZE = 20
/** 编排面板里搜索视频的结果条数：够用即可，避免把整页撑太长 */
const SEARCH_SIZE = 10

const collections = ref<AdminCollection[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(true)
const error = ref('')
/** 单行操作失败时的行内提示，不覆盖整页 */
const actionError = ref('')
/** 正在切换状态的合集 id，用于禁用按钮并给出「处理中」反馈 */
const pendingId = ref('')

/** 新建 / 编辑弹框 */
const editOpen = ref(false)
const editing = ref<AdminCollection | null>(null)
const editSaving = ref(false)
const editError = ref('')

/** 删除确认弹框 */
const deleteOpen = ref(false)
const deleting = ref<AdminCollection | null>(null)
const deleteSaving = ref(false)
const deleteError = ref('')

/** 编排面板：非空即表示当前处于面板形态 */
const panelCollection = ref<AdminCollection | null>(null)
const panelVideos = ref<Video[]>([])
const panelLoading = ref(false)
const panelError = ref('')
/** 添加 / 移除进行中的行内提示与按钮禁用 */
const panelPending = ref(false)
const panelActionError = ref('')
/** 本地顺序是否已被改动（增删即时生效，不算） */
const panelDirty = ref(false)
const panelSaving = ref(false)
const panelSaveError = ref('')
const panelSaveNotice = ref('')

/** 搜索视频 */
const searchInput = ref('')
const searchKeyword = ref('')
const searchResults = ref<Video[]>([])
const searchLoading = ref(false)
const searchError = ref('')
/** 按 ID 添加的输入框 */
const idInput = ref('')

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

function initialOf(title: string): string {
  return title.trim().charAt(0) || '窗'
}

function statusText(status: number): string {
  return status === COLLECTION_STATUS_PUBLISHED ? '已发布' : '草稿'
}

function statusClass(status: number): string {
  return status === COLLECTION_STATUS_PUBLISHED ? 'is-published' : 'is-draft'
}

/** 表格里的时间用绝对时间，相对时间在管理场景里不够精确 */
function formatDateTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return '—'
  }
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`
}

function categoryName(id: number): string {
  return VIDEO_CATEGORIES.find((item) => item.id === id)?.name ?? '未分类'
}

async function load(targetPage: number): Promise<void> {
  loading.value = true
  error.value = ''
  actionError.value = ''

  try {
    const result = await listAdminCollections({ page: targetPage, page_size: PAGE_SIZE })
    collections.value = result.items ?? []
    total.value = result.total ?? 0
    page.value = result.page ?? targetPage
  } catch (e) {
    error.value = toErrorMessage(e)
    collections.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

/** 更新单行的数据；若该行正在编排面板里，同步面板标题等字段 */
function replaceRow(updated: AdminCollection): void {
  const index = collections.value.findIndex((item) => item.id === updated.id)
  if (index >= 0) {
    collections.value[index] = updated
  }
  if (panelCollection.value?.id === updated.id) {
    panelCollection.value = { ...panelCollection.value, ...updated }
  }
}

/** 视频数变化后同步列表行与面板标题，避免还显示旧数字 */
function applyVideoCount(collectionId: string, count: number): void {
  const index = collections.value.findIndex((item) => item.id === collectionId)
  if (index >= 0) {
    collections.value[index] = { ...collections.value[index], video_count: count }
  }
  if (panelCollection.value?.id === collectionId) {
    panelCollection.value = { ...panelCollection.value, video_count: count }
  }
}

function openCreate(): void {
  editing.value = null
  editError.value = ''
  editOpen.value = true
}

function openEdit(item: AdminCollection): void {
  editing.value = item
  editError.value = ''
  editOpen.value = true
}

function closeEdit(): void {
  editOpen.value = false
  editing.value = null
  editError.value = ''
}

/**
 * 保存合集信息：新建后回到第一页（新合集总在第一页顶部），
 * 编辑后就地替换该行，不整页重拉。
 */
async function submitEdit(payload: CreateCollectionPayload): Promise<void> {
  if (editSaving.value) {
    return
  }
  editSaving.value = true
  editError.value = ''

  try {
    if (editing.value) {
      const updated = await updateCollection(editing.value.id, payload)
      replaceRow(updated)
    } else {
      await createCollection(payload)
      await load(1)
    }
    closeEdit()
  } catch (e) {
    // 失败时保留弹框，让用户能改完重试，输入不丢
    editError.value = toErrorMessage(e)
  } finally {
    editSaving.value = false
  }
}

/** 发布 / 转草稿：草稿不会被公开合集接口返回 */
async function toggleStatus(item: AdminCollection): Promise<void> {
  if (pendingId.value) {
    return
  }
  const next =
    item.status === COLLECTION_STATUS_PUBLISHED
      ? COLLECTION_STATUS_DRAFT
      : COLLECTION_STATUS_PUBLISHED

  pendingId.value = item.id
  actionError.value = ''
  try {
    const updated = await updateCollection(item.id, { status: next })
    replaceRow(updated)
  } catch (e) {
    actionError.value = toErrorMessage(e)
  } finally {
    pendingId.value = ''
  }
}

function askDelete(item: AdminCollection): void {
  deleting.value = item
  deleteError.value = ''
  deleteOpen.value = true
}

function closeDelete(): void {
  deleteOpen.value = false
  deleting.value = null
  deleteError.value = ''
}

/** 删除合集不会删除视频本身，只解除合集与视频的关联 */
async function confirmDelete(): Promise<void> {
  if (!deleting.value || deleteSaving.value) {
    return
  }
  const target = deleting.value
  deleteSaving.value = true
  deleteError.value = ''

  try {
    await deleteCollection(target.id)
    collections.value = collections.value.filter((item) => item.id !== target.id)
    total.value = Math.max(0, total.value - 1)
    closeDelete()

    if (collections.value.length === 0 && page.value > 1) {
      await load(page.value - 1)
    }
  } catch (e) {
    deleteError.value = toErrorMessage(e)
  } finally {
    deleteSaving.value = false
  }
}

// ===== 编排视频面板 =====

async function openPanel(item: AdminCollection): Promise<void> {
  panelCollection.value = item
  panelVideos.value = []
  panelLoading.value = true
  panelError.value = ''
  panelActionError.value = ''
  panelDirty.value = false
  panelSaveError.value = ''
  panelSaveNotice.value = ''
  searchInput.value = ''
  searchKeyword.value = ''
  searchResults.value = []
  searchError.value = ''
  idInput.value = ''

  try {
    // 管理端详情：含草稿合集与未上架视频，顺序与库内一致，重排才不会被判「集合不一致」
    const detail = await getAdminCollection(item.id)
    panelVideos.value = detail.videos ?? []
    applyVideoCount(item.id, panelVideos.value.length)
  } catch (e) {
    panelError.value = toErrorMessage(e)
  } finally {
    panelLoading.value = false
  }
}

/** 面板加载失败后的重试：保留当前合集，重新拉取视频 */
function retryPanel(): void {
  if (panelCollection.value) {
    void openPanel(panelCollection.value)
  }
}

function closePanel(): void {
  panelCollection.value = null
  panelVideos.value = []
  panelLoading.value = false
  panelError.value = ''
  panelActionError.value = ''
  panelDirty.value = false
  panelSaveError.value = ''
  panelSaveNotice.value = ''
  searchInput.value = ''
  searchKeyword.value = ''
  searchResults.value = []
  searchError.value = ''
  idInput.value = ''
}

/** 已加入合集的视频不再重复添加 */
function isInPanel(videoId: string): boolean {
  return panelVideos.value.some((item) => item.id === videoId)
}

async function submitSearch(): Promise<void> {
  const keyword = searchInput.value.trim()
  searchKeyword.value = keyword
  searchError.value = ''
  panelActionError.value = ''

  if (!keyword) {
    searchResults.value = []
    return
  }

  searchLoading.value = true
  try {
    // 管理端搜索能搜到未上架视频，合集可以先编排、后发布
    const result = await listAdminVideos({ page: 1, page_size: SEARCH_SIZE, keyword })
    searchResults.value = result.list ?? []
  } catch (e) {
    searchError.value = toErrorMessage(e)
    searchResults.value = []
  } finally {
    searchLoading.value = false
  }
}

async function addVideo(video: Video): Promise<void> {
  const target = panelCollection.value
  if (!target || panelPending.value || isInPanel(video.id)) {
    return
  }
  panelPending.value = true
  panelActionError.value = ''

  try {
    await addCollectionVideos(target.id, [video.id])
    panelVideos.value = [...panelVideos.value, video]
    applyVideoCount(target.id, panelVideos.value.length)
    panelSaveNotice.value = ''
  } catch (e) {
    panelActionError.value = toErrorMessage(e)
  } finally {
    panelPending.value = false
  }
}

/** 按 ID 添加：先取一次视频详情校验存在性，再调添加接口 */
async function addById(): Promise<void> {
  const target = panelCollection.value
  const id = idInput.value.trim()
  if (!target || panelPending.value) {
    return
  }
  if (!id) {
    panelActionError.value = '请输入视频 ID'
    return
  }
  if (isInPanel(id)) {
    panelActionError.value = '这个视频已经在合集里了'
    return
  }

  panelPending.value = true
  panelActionError.value = ''
  try {
    const video = await getVideo(id)
    await addCollectionVideos(target.id, [video.id])
    panelVideos.value = [...panelVideos.value, video]
    applyVideoCount(target.id, panelVideos.value.length)
    idInput.value = ''
    panelSaveNotice.value = ''
  } catch (e) {
    panelActionError.value = toErrorMessage(e)
  } finally {
    panelPending.value = false
  }
}

async function removeVideo(video: Video): Promise<void> {
  const target = panelCollection.value
  if (!target || panelPending.value) {
    return
  }
  panelPending.value = true
  panelActionError.value = ''

  try {
    await removeCollectionVideo(target.id, video.id)
    panelVideos.value = panelVideos.value.filter((item) => item.id !== video.id)
    applyVideoCount(target.id, panelVideos.value.length)
    panelSaveNotice.value = ''
  } catch (e) {
    panelActionError.value = toErrorMessage(e)
  } finally {
    panelPending.value = false
  }
}

/** 上移 / 下移只改本地数组，保存时才提交顺序 */
function move(index: number, delta: number): void {
  const target = index + delta
  if (target < 0 || target >= panelVideos.value.length) {
    return
  }
  const next = [...panelVideos.value]
  const [item] = next.splice(index, 1)
  next.splice(target, 0, item)
  panelVideos.value = next
  panelDirty.value = true
  panelSaveNotice.value = ''
}

async function saveOrder(): Promise<void> {
  const target = panelCollection.value
  // 空合集没有顺序可存，后端也会把空 video_ids 当成参数错误
  if (!target || panelSaving.value || panelVideos.value.length === 0) {
    return
  }
  panelSaving.value = true
  panelSaveError.value = ''
  panelSaveNotice.value = ''

  try {
    await reorderCollectionVideos(
      target.id,
      panelVideos.value.map((item) => item.id),
    )
    panelDirty.value = false
    panelSaveNotice.value = '排序已保存'
  } catch (e) {
    panelSaveError.value = toErrorMessage(e)
  } finally {
    panelSaving.value = false
  }
}

onMounted(() => void load(1))
</script>

<template>
  <div class="admin cy-container">
    <header class="admin__head">
      <div class="admin__head-main">
        <h1 class="cy-title">合集管理</h1>
        <p v-if="!panelCollection && !loading && !error" class="cy-caption">
          共 <span class="cy-num">{{ total }}</span> 个合集
        </p>
      </div>
      <button
        v-if="!panelCollection"
        class="cy-btn cy-btn--primary cy-btn--sm"
        type="button"
        @click="openCreate"
      >
        新建合集
      </button>
    </header>

    <!-- ===== 编排视频面板 ===== -->
    <section v-if="panelCollection" class="panel">
      <div class="panel__head">
        <button class="cy-btn cy-btn--ghost cy-btn--sm" type="button" @click="closePanel">
          ← 返回合集列表
        </button>
        <div class="panel__head-info">
          <h2 class="cy-subtitle">{{ panelCollection.title }}</h2>
          <p class="cy-caption">
            共 <span class="cy-num">{{ panelVideos.length }}</span> 个视频 · 上移 / 下移后请点「保存排序」
          </p>
        </div>
      </div>

      <div v-if="panelLoading" class="panel__skeleton" aria-hidden="true">
        <div v-for="index in 4" :key="index" class="panel__sk-row cy-skeleton" />
      </div>

      <StatePanel
        v-else-if="panelError"
        variant="error"
        title="合集内容加载失败"
        :description="panelError"
        action-label="重新加载"
        @action="retryPanel"
      />

      <div v-else class="panel__body">
        <div class="panel__add">
          <h3 class="cy-label">添加视频</h3>

          <form class="panel__search" role="search" @submit.prevent="submitSearch">
            <input
              v-model="searchInput"
              class="cy-input"
              type="search"
              placeholder="搜索标题 / 简介"
              aria-label="搜索要添加的视频"
            />
            <button class="cy-btn cy-btn--secondary cy-btn--sm" type="submit" :disabled="searchLoading">
              {{ searchLoading ? '搜索中…' : '搜索' }}
            </button>
          </form>

          <form class="panel__id" @submit.prevent="addById">
            <input
              v-model="idInput"
              class="cy-input"
              type="text"
              inputmode="numeric"
              placeholder="输入视频 ID 直接添加"
              aria-label="按视频 ID 添加"
            />
            <button class="cy-btn cy-btn--secondary cy-btn--sm" type="submit" :disabled="panelPending">
              添加
            </button>
          </form>

          <p v-if="panelActionError" class="cy-alert cy-alert--error panel__alert">
            {{ panelActionError }}
          </p>

          <div v-if="searchError" class="cy-alert cy-alert--error panel__alert">
            {{ searchError }}
          </div>

          <div v-if="searchResults.length > 0" class="panel__results">
            <div v-for="video in searchResults" :key="video.id" class="search-row">
              <div class="search-row__cover">
                <img
                  v-if="video.cover_url"
                  :src="video.cover_url"
                  :alt="video.title"
                  loading="lazy"
                />
                <span v-else aria-hidden="true">{{ initialOf(video.title) }}</span>
              </div>
              <div class="search-row__info">
                <p class="search-row__title cy-clamp-2">{{ video.title }}</p>
                <p class="cy-caption">
                  {{ categoryName(video.category_id) }} ·
                  <span class="cy-num">{{ formatCount(video.view_count) }}</span> 次播放
                </p>
              </div>
              <button
                class="cy-btn cy-btn--outline cy-btn--sm"
                type="button"
                :disabled="panelPending || isInPanel(video.id)"
                @click="addVideo(video)"
              >
                {{ isInPanel(video.id) ? '已添加' : '添加' }}
              </button>
            </div>
          </div>

          <p
            v-else-if="searchKeyword && !searchLoading && !searchError"
            class="cy-caption panel__hint"
          >
            没有匹配的视频，换个关键词或直接用 ID 添加。
          </p>
        </div>

        <div class="panel__list">
          <div class="panel__list-head">
            <h3 class="cy-label">合集内视频</h3>
            <div class="panel__save">
              <span v-if="panelDirty" class="cy-caption">顺序有改动</span>
              <span v-else-if="panelSaveNotice" class="cy-alert cy-alert--success panel__notice">
                {{ panelSaveNotice }}
              </span>
              <button
                class="cy-btn cy-btn--primary cy-btn--sm"
                type="button"
                :disabled="panelSaving || panelPending || !panelDirty || panelVideos.length === 0"
                @click="saveOrder"
              >
                {{ panelSaving ? '保存中…' : '保存排序' }}
              </button>
            </div>
          </div>

          <p v-if="panelSaveError" class="cy-alert cy-alert--error panel__alert">
            {{ panelSaveError }}
          </p>

          <StatePanel
            v-if="panelVideos.length === 0"
            title="合集里还没有视频"
            description="从左侧搜索或按 ID 添加视频，加到合集里的内容会按这里的顺序播放。"
          />

          <div v-else class="panel__rows">
            <div v-for="(video, index) in panelVideos" :key="video.id" class="order-row">
              <span class="order-row__index cy-num">{{ index + 1 }}</span>

              <div class="order-row__cover">
                <img
                  v-if="video.cover_url"
                  :src="video.cover_url"
                  :alt="video.title"
                  loading="lazy"
                />
                <span v-else aria-hidden="true">{{ initialOf(video.title) }}</span>
              </div>

              <div class="order-row__info">
                <p class="order-row__title cy-clamp-2">{{ video.title }}</p>
                <p class="cy-caption">
                  {{ categoryName(video.category_id) }} ·
                  <span class="cy-num">{{ formatCount(video.view_count) }}</span> 次播放
                </p>
              </div>

              <div class="order-row__actions">
                <button
                  class="cy-btn cy-btn--outline cy-btn--sm"
                  type="button"
                  :disabled="panelPending || index === 0"
                  @click="move(index, -1)"
                >
                  上移
                </button>
                <button
                  class="cy-btn cy-btn--outline cy-btn--sm"
                  type="button"
                  :disabled="panelPending || index === panelVideos.length - 1"
                  @click="move(index, 1)"
                >
                  下移
                </button>
                <button
                  class="cy-btn cy-btn--outline cy-btn--sm admin__danger"
                  type="button"
                  :disabled="panelPending"
                  @click="removeVideo(video)"
                >
                  移除
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- ===== 合集列表 ===== -->
    <template v-else>
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
        title="合集加载失败"
        :description="error"
        action-label="重新加载"
        @action="load(page)"
      />

      <StatePanel
        v-else-if="collections.length === 0"
        title="还没有合集"
        description="新建一个合集，把系列视频按顺序编排到一起。"
        action-label="新建合集"
        @action="openCreate"
      />

      <template v-else>
        <div class="admin__table">
          <div class="admin__row admin__row--head">
            <span>合集</span>
            <span class="admin__cell--num">视频数</span>
            <span>状态</span>
            <span class="admin__cell--time">创建时间</span>
            <span class="admin__cell--action">操作</span>
          </div>

          <div v-for="item in collections" :key="item.id" class="admin__row">
            <div class="admin__main">
              <div class="admin__cover">
                <img
                  v-if="item.cover_url"
                  :src="item.cover_url"
                  :alt="item.title"
                  loading="lazy"
                />
                <span v-else class="admin__cover-empty" aria-hidden="true">
                  {{ initialOf(item.title) }}
                </span>
              </div>
              <div class="admin__info">
                <RouterLink
                  class="admin__title cy-clamp-2"
                  :to="{ name: 'collection', params: { id: item.id } }"
                >
                  {{ item.title }}
                </RouterLink>
                <p v-if="item.description" class="admin__desc cy-clamp-1 cy-caption">
                  {{ item.description }}
                </p>
              </div>
            </div>

            <span class="admin__cell--num cy-num">{{ item.video_count }}</span>

            <span class="admin__status" :class="statusClass(item.status)">
              {{ statusText(item.status) }}
            </span>

            <span class="admin__cell--time cy-caption cy-num">
              {{ formatDateTime(item.created_at) }}
            </span>

            <span class="admin__cell--action admin__actions">
              <button
                class="cy-btn cy-btn--outline cy-btn--sm"
                type="button"
                :disabled="pendingId === item.id"
                @click="openPanel(item)"
              >
                编排视频
              </button>
              <button
                class="cy-btn cy-btn--outline cy-btn--sm"
                type="button"
                :disabled="pendingId === item.id"
                @click="openEdit(item)"
              >
                编辑
              </button>
              <button
                class="cy-btn cy-btn--outline cy-btn--sm"
                type="button"
                :disabled="pendingId === item.id"
                @click="toggleStatus(item)"
              >
                {{
                  pendingId === item.id
                    ? '处理中…'
                    : item.status === COLLECTION_STATUS_PUBLISHED
                      ? '转草稿'
                      : '发布'
                }}
              </button>
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
    </template>

    <CollectionEditDialog
      :open="editOpen"
      :collection="editing"
      :saving="editSaving"
      :error="editError"
      @close="closeEdit"
      @submit="submitEdit"
    />

    <ConfirmDialog
      :open="deleteOpen"
      title="删除这个合集？"
      :description="`「${deleting?.title ?? ''}」将被删除，合集里的视频本身不会受影响。`"
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
  align-items: center;
  justify-content: space-between;
  gap: var(--cy-space-4);
  padding: var(--cy-space-5) 0 var(--cy-space-4);
}

.admin__head-main {
  display: flex;
  align-items: baseline;
  gap: var(--cy-space-3);
  min-width: 0;
}

.admin__alert {
  margin-bottom: var(--cy-space-4);
}

/* 表格：五列网格，表头与数据行共用同一套列宽 */
.admin__table {
  border-top: 1px solid var(--cy-border);
}

.admin__row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 80px 84px 140px 320px;
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

/* 无封面：星紫纯色平涂 + 月白首字，与视频管理保持一致 */
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

.admin__cell--num,
.admin__cell--time {
  color: var(--cy-text-secondary);
  font-size: 13px;
}

.admin__cell--action {
  min-width: 0;
}

.admin__actions {
  display: flex;
  justify-content: flex-start;
  gap: var(--cy-space-2);
}

/* 删除是低频且有破坏性的操作：只把文字与描边换成危险色 */
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

.admin__status.is-draft {
  background: var(--cy-surface-2);
  color: var(--cy-text-muted);
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

/* ===== 编排面板 ===== */
.panel__head {
  display: flex;
  align-items: flex-start;
  gap: var(--cy-space-4);
  padding-bottom: var(--cy-space-4);
  border-bottom: 1px solid var(--cy-border);
}

.panel__head-info {
  min-width: 0;
}

.panel__skeleton {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-3);
  padding-top: var(--cy-space-4);
}

.panel__sk-row {
  height: 64px;
  border-radius: var(--cy-radius-sm);
}

/* 左侧添加、右侧排序；窄屏改为上下排 */
.panel__body {
  display: grid;
  grid-template-columns: 320px minmax(0, 1fr);
  gap: var(--cy-space-6);
  padding-top: var(--cy-space-5);
}

.panel__add {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-3);
  align-self: start;
}

.panel__search,
.panel__id {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
}

.panel__search .cy-input,
.panel__id .cy-input {
  flex: 1;
  min-width: 0;
}

.panel__alert {
  margin-top: var(--cy-space-1);
}

.panel__hint {
  margin-top: var(--cy-space-1);
}

.panel__results {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-2);
  max-height: 420px;
  overflow-y: auto;
  padding-top: var(--cy-space-2);
  border-top: 1px solid var(--cy-border);
}

.search-row {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  padding: var(--cy-space-1);
  border-radius: var(--cy-radius-sm);
}

.search-row:hover {
  background: var(--cy-surface-2);
}

.search-row__cover {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 72px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-xs);
  overflow: hidden;
  background: var(--cy-primary);
  color: var(--cy-on-primary);
  font-size: 14px;
  font-weight: 600;
}

.search-row__cover img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.search-row__info {
  flex: 1;
  min-width: 0;
}

.search-row__title {
  color: var(--cy-text);
  font-size: 13px;
  line-height: 18px;
  font-weight: 500;
}

.panel__list {
  min-width: 0;
}

.panel__list-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--cy-space-3);
  padding-bottom: var(--cy-space-3);
  border-bottom: 1px solid var(--cy-border);
}

.panel__save {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
}

/* 成功提示只在这里用一次：给「排序已保存」一个轻量反馈 */
.panel__notice {
  margin: 0;
  padding: var(--cy-space-1) var(--cy-space-3);
}

.panel__rows {
  display: flex;
  flex-direction: column;
}

.order-row {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  padding: var(--cy-space-2) 0;
  border-bottom: 1px solid var(--cy-border);
}

.order-row__index {
  flex-shrink: 0;
  width: 24px;
  color: var(--cy-text-muted);
  font-size: 13px;
  text-align: center;
}

.order-row__cover {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 80px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-xs);
  overflow: hidden;
  background: var(--cy-primary);
  color: var(--cy-on-primary);
  font-size: 14px;
  font-weight: 600;
}

.order-row__cover img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.order-row__info {
  flex: 1;
  min-width: 0;
}

.order-row__title {
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
  font-weight: 500;
}

.order-row__actions {
  display: flex;
  flex-shrink: 0;
  gap: var(--cy-space-2);
}

@media (max-width: 1199px) {
  .panel__body {
    grid-template-columns: minmax(0, 1fr);
  }
}

/* 窄屏：隐藏次要列，表格降级成「封面 + 标题 + 状态 + 操作」 */
@media (max-width: 1023px) {
  .admin__row {
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: var(--cy-space-3);
  }

  .admin__row--head,
  .admin__cell--num,
  .admin__cell--time {
    display: none;
  }
}

@media (max-width: 599px) {
  .order-row {
    flex-wrap: wrap;
  }

  .order-row__actions {
    flex-basis: 100%;
    justify-content: flex-end;
  }
}
</style>
