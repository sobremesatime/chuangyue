<script setup lang="ts">
// 管理后台 /admin/reports：举报处理。
//
// 用户对视频或评论发起举报后进入这里；管理员按状态筛选并逐条处理：
// 「已处理」表示举报属实并已跟进，「已忽略」表示经核实不予处理，两者都不可再改。
// 处理后就地更新该行，不整页重拉（筛选条件可能因此变化时再按需修正）。
//
// 状态筛选 pill 与管理后台其它页面同款；状态标签是列表里唯一的语义色，
// 其余一律走中性面。管理员页面，入口在侧边导航「管理」分组。

import { computed, onMounted, ref } from 'vue'

import { deleteAdminComment } from '@/api/comment'
import { handleReport, listAdminReports } from '@/api/report'
import { ApiError, toErrorMessage } from '@/api/request'
import { deleteVideo } from '@/api/video'
import StatePanel from '@/components/StatePanel.vue'
import {
  REPORT_STATUS_HANDLED,
  REPORT_STATUS_IGNORED,
  REPORT_STATUS_PENDING,
  REPORT_TARGET_VIDEO,
  type Report,
} from '@/types/api'

const PAGE_SIZE = 20

interface StatusFilter {
  label: string
  /** 不传表示不过滤 */
  value?: number
}

const statusFilters: StatusFilter[] = [
  { label: '全部' },
  { label: '待处理', value: REPORT_STATUS_PENDING },
  { label: '已处理', value: REPORT_STATUS_HANDLED },
  { label: '已忽略', value: REPORT_STATUS_IGNORED },
]

const reports = ref<Report[]>([])
const total = ref(0)
const page = ref(1)
const statusFilter = ref<number | undefined>(undefined)

const loading = ref(true)
const error = ref('')
/** 处理失败时的行内提示，不覆盖整页 */
const actionError = ref('')
/** 正在处理的举报 id，用于禁用按钮并给出「处理中」反馈 */
const pendingId = ref('')

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

/** 表格里的时间用绝对时间，相对时间在管理场景里不够精确；未处理时 handled_at 为空 */
function formatDate(value?: string | null): string {
  if (!value) {
    return '—'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return '—'
  }
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`
}

/** 评论举报没有视频标题可用时，用 id 尾部做摘要，避免整串雪花 ID 撑破列 */
function shortId(id: string): string {
  return id.length > 8 ? id.slice(-6) : id
}

function isVideo(r: Report): boolean {
  return r.target_type === REPORT_TARGET_VIDEO
}

function statusText(status: number): string {
  if (status === REPORT_STATUS_HANDLED) {
    return '已处理'
  }
  if (status === REPORT_STATUS_IGNORED) {
    return '已忽略'
  }
  return '待处理'
}

function statusClass(status: number): string {
  if (status === REPORT_STATUS_HANDLED) {
    return 'is-handled'
  }
  if (status === REPORT_STATUS_IGNORED) {
    return 'is-ignored'
  }
  return 'is-pending'
}

async function load(targetPage: number): Promise<void> {
  loading.value = true
  error.value = ''
  actionError.value = ''

  try {
    const result = await listAdminReports({
      page: targetPage,
      page_size: PAGE_SIZE,
      status: statusFilter.value,
    })
    reports.value = result.list ?? []
    total.value = result.total ?? 0
    page.value = result.page ?? targetPage
  } catch (e) {
    error.value = toErrorMessage(e)
    reports.value = []
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

/**
 * 把处理结果写回列表。
 *
 * 若当前处于某个状态筛选下，处理后该行不再符合条件，就地移除并修正总数，
 * 避免出现「筛待处理却还显示已处理」的错位；无筛选时用返回值原地更新该行。
 */
function applyReportUpdate(updated: Report): void {
  const index = reports.value.findIndex((item) => item.id === updated.id)
  if (index < 0) {
    return
  }
  if (statusFilter.value !== undefined && statusFilter.value !== updated.status) {
    reports.value.splice(index, 1)
    total.value = Math.max(0, total.value - 1)
  } else {
    reports.value[index] = updated
  }
}

/**
 * 处理举报：1 已处理、2 已忽略。
 */
async function handle(report: Report, status: 1 | 2): Promise<void> {
  if (pendingId.value) {
    return
  }
  pendingId.value = report.id
  actionError.value = ''
  try {
    const updated = await handleReport(report.id, status)
    applyReportUpdate(updated)
  } catch (e) {
    actionError.value = toErrorMessage(e)
  } finally {
    pendingId.value = ''
  }
}

/**
 * 删除被举报内容并标记已处理。
 *
 * 为什么是两次调用而不是一个聚合接口：被举报对象分属两个域（视频在视频域、评论在互动域），
 * 聚合要么让网关做跨域编排（它目前只是 HTTP↔gRPC 翻译层），要么让视频域反向调用互动域
 * （互动域本来就依赖视频域，会形成环）。前端按 target_type 调对应域的删除接口再标记举报，
 * 失败时会停在「内容已删、举报待处理」——管理员再点一次即可，删除接口对不存在的对象按已删除处理。
 */
async function handleWithDelete(report: Report): Promise<void> {
  if (pendingId.value) {
    return
  }
  pendingId.value = report.id
  actionError.value = ''
  try {
    await deleteReportedContent(report)
    const updated = await handleReport(report.id, REPORT_STATUS_HANDLED)
    applyReportUpdate(updated)
  } catch (e) {
    actionError.value = toErrorMessage(e)
  } finally {
    pendingId.value = ''
  }
}

/** 按举报对象类型删除对应内容；对象已不存在时视为已删除，不阻断后续标记。 */
async function deleteReportedContent(report: Report): Promise<void> {
  try {
    if (isVideo(report)) {
      await deleteVideo(report.target_id)
    } else {
      await deleteAdminComment(report.target_id)
    }
  } catch (e) {
    // 30001 视频不存在 / 30006 评论不存在：可能已被其他管理员处理过
    if (!(e instanceof ApiError) || (e.code !== 30001 && e.code !== 30006)) {
      throw e
    }
  }
}

onMounted(() => void load(1))
</script>

<template>
  <div class="admin cy-container">
    <header class="admin__head">
      <h1 class="cy-title">举报处理</h1>
      <p v-if="!loading && !error" class="cy-caption">
        共 <span class="cy-num">{{ total }}</span> 条举报
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

    <p v-if="actionError" class="cy-alert cy-alert--error admin__alert">{{ actionError }}</p>

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
      title="举报加载失败"
      :description="error"
      action-label="重新加载"
      @action="load(page)"
    />

    <StatePanel
      v-else-if="reports.length === 0"
      title="这里还没有举报"
      :description="
        statusFilter === undefined
          ? '暂时没有收到任何举报。'
          : '当前状态下没有举报，换个筛选看看。'
      "
    />

    <template v-else>
      <div class="admin__table">
        <div class="admin__row admin__row--head">
          <span>被举报对象</span>
          <span class="admin__cell--reason">理由</span>
          <span class="admin__cell--detail">补充说明</span>
          <span class="admin__cell--reporter">举报人</span>
          <span class="admin__cell--time">时间</span>
          <span class="admin__cell--status">状态</span>
          <span class="admin__cell--action">操作</span>
        </div>

        <div v-for="report in reports" :key="report.id" class="admin__row">
          <div class="admin__target">
            <RouterLink
              v-if="report.video_id"
              class="admin__link"
              :to="{ name: 'video-detail', params: { id: report.video_id } }"
            >
              {{ isVideo(report) ? `视频：${report.video_title}` : `评论 #${shortId(report.target_id)}` }}
            </RouterLink>
            <span v-else class="admin__text">
              {{ isVideo(report) ? `视频：${report.video_title}` : `评论 #${shortId(report.target_id)}` }}
            </span>
          </div>

          <span class="admin__cell--reason">{{ report.reason }}</span>

          <span class="admin__cell--detail cy-clamp-2">{{ report.detail || '—' }}</span>

          <span class="admin__cell--reporter cy-clamp-1">{{ report.reporter_nickname }}</span>

          <span class="admin__cell--time cy-caption cy-num">{{ formatDate(report.created_at) }}</span>

          <span class="admin__status" :class="statusClass(report.status)">
            {{ statusText(report.status) }}
          </span>

          <span class="admin__cell--action admin__actions">
            <template v-if="report.status === REPORT_STATUS_PENDING">
              <!-- 一次把「删内容 + 标记已处理」做完：否则管理员得跳去另一个页面手工删，容易漏 -->
              <button
                class="cy-btn cy-btn--outline cy-btn--sm admin__danger"
                type="button"
                :disabled="pendingId === report.id"
                @click="handleWithDelete(report)"
              >
                删除并处理
              </button>
              <button
                class="cy-btn cy-btn--outline cy-btn--sm"
                type="button"
                :disabled="pendingId === report.id"
                @click="handle(report, REPORT_STATUS_HANDLED)"
              >
                {{ pendingId === report.id ? '处理中…' : '已处理' }}
              </button>
              <button
                class="cy-btn cy-btn--outline cy-btn--sm"
                type="button"
                :disabled="pendingId === report.id"
                @click="handle(report, REPORT_STATUS_IGNORED)"
              >
                忽略
              </button>
            </template>
            <span v-else class="cy-caption admin__handled-at cy-num">
              {{ formatDate(report.handled_at) }}
            </span>
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

.admin__alert {
  margin-bottom: var(--cy-space-4);
}

/* 表格：七列网格，表头与数据行共用同一套列宽 */
.admin__table {
  border-top: 1px solid var(--cy-border);
}

.admin__row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 96px 180px 120px 140px 80px 172px;
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

.admin__target {
  min-width: 0;
}

.admin__text {
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
}

.admin__link {
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
  font-weight: 500;
}

.admin__link:hover {
  color: var(--cy-accent);
}

.admin__cell--reason {
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 18px;
}

.admin__cell--detail {
  color: var(--cy-text-muted);
  font-size: 13px;
  line-height: 18px;
}

.admin__cell--reporter,
.admin__cell--time {
  min-width: 0;
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 18px;
}

.admin__cell--action {
  min-width: 0;
}

/* 操作列：按钮左对齐排成一行；已处理时只显示处理时间 */
.admin__actions {
  display: flex;
  justify-content: flex-start;
  gap: var(--cy-space-2);
}

/* 删除是破坏性操作：只把文字换成危险色，不铺底色（与视频管理一致） */
.admin__danger {
  color: var(--cy-danger);
}

.admin__actions .admin__danger:hover:not(:disabled) {
  border-color: var(--cy-danger);
  color: var(--cy-danger);
}

.admin__handled-at {
  color: var(--cy-text-muted);
}

/* 状态标签：整张表唯一的语义色，其余一律中性 */
.admin__status {
  justify-self: start;
  height: 24px;
  padding: 0 10px;
  border-radius: var(--cy-radius-full);
  font-size: 12px;
  line-height: 24px;
  white-space: nowrap;
}

.admin__status.is-pending {
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

.admin__status.is-handled {
  background: var(--cy-surface-2);
  color: var(--cy-text-secondary);
}

.admin__status.is-ignored {
  background: var(--cy-surface-2);
  color: var(--cy-text-muted);
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

/* 窄屏：隐藏次要列，表格降级成「被举报对象 + 状态 + 操作」 */
@media (max-width: 899px) {
  .admin__row {
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: var(--cy-space-3);
  }

  .admin__row--head,
  .admin__cell--reason,
  .admin__cell--detail,
  .admin__cell--reporter,
  .admin__cell--time {
    display: none;
  }
}
</style>