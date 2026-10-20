<script setup lang="ts">
// 顶栏通知铃铛：未读红点 + 下拉消息面板。
//
// 只做站内两类提醒——「你的评论被点赞」「你的视频收到新评论」，均来自后端 notification 表。
// 未读数每 30 秒轮询一次；打开面板时拉取最新列表，点某条通知即标记为已读并跳到对应视频。

import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import { fetchUnreadCount, listNotifications, markNotificationsRead } from '@/api/notification'
import AppIcon from '@/components/AppIcon.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import type { AppNotification } from '@/types/api'
import { formatRelativeDate } from '@/utils/format'

const router = useRouter()

/** 每页拉取的通知条数：面板是轻量预览，不做无限滚动 */
const PAGE_SIZE = 20
/** 未读数轮询间隔（毫秒） */
const POLL_INTERVAL = 30000

const open = ref(false)
const unread = ref(0)
const items = ref<AppNotification[]>([])
const loading = ref(false)
const boxRef = ref<HTMLElement | null>(null)
const bellRef = ref<HTMLButtonElement | null>(null)

let pollTimer = 0

/** 通知文案：按类型拼出「谁 + 做了什么」 */
function actionText(type: number): string {
  switch (type) {
    case 1:
      return '赞了你的评论'
    case 3:
      return '回复了你的评论'
    // 管理动作：不展示管理员昵称，用完整句子说明「内容去哪了」
    case 4:
      return '你的评论因违反社区规范已被删除'
    case 5:
      return '你的弹幕因违反社区规范已被删除'
    default:
      return '评论了你的视频'
  }
}

/** 管理动作类通知：不显示触发者昵称（那是对普通用户无意义的内部账号） */
function isSystemNotice(type: number): boolean {
  return type === 4 || type === 5
}

function nameOf(item: AppNotification): string {
  return item.nickname || '已注销用户'
}

/** 轮询未读数：失败静默，避免偶发网络抖动打扰用户 */
async function refreshUnread(): Promise<void> {
  try {
    const result = await fetchUnreadCount()
    unread.value = result.unread_count
  } catch {
    // 忽略：红点不是关键路径
  }
}

async function loadList(): Promise<void> {
  loading.value = true
  try {
    const page = await listNotifications({ page: 1, page_size: PAGE_SIZE })
    items.value = page.list
    await refreshUnread()
  } catch {
    // 忽略：面板会展示空态
  } finally {
    loading.value = false
  }
}

async function toggle(): Promise<void> {
  open.value = !open.value
  if (open.value) {
    await loadList()
  }
}

function close(): void {
  open.value = false
}

/** 点开某条通知：先标记已读，再跳到对应视频详情页 */
async function openItem(item: AppNotification): Promise<void> {
  close()
  if (!item.is_read) {
    try {
      const result = await markNotificationsRead(item.id)
      unread.value = result.unread_count
      item.is_read = true
    } catch {
      // 忽略：标记失败不拦跳转
    }
  }
  await router.push({ name: 'video-detail', params: { id: item.video_id } })
}

/** 一键全部已读 */
async function markAll(): Promise<void> {
  try {
    const result = await markNotificationsRead()
    unread.value = result.unread_count
    items.value = items.value.map((item) => ({ ...item, is_read: true }))
  } catch {
    // 忽略
  }
}

/** 点面板与铃铛之外收起 */
function onDocumentClick(event: MouseEvent): void {
  if (!open.value) {
    return
  }
  const target = event.target as Node | null
  if (target && boxRef.value?.contains(target)) {
    return
  }
  close()
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && open.value) {
    close()
    bellRef.value?.focus()
  }
}

onMounted(() => {
  void refreshUnread()
  pollTimer = window.setInterval(() => void refreshUnread(), POLL_INTERVAL)
  document.addEventListener('click', onDocumentClick)
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  window.clearInterval(pollTimer)
  document.removeEventListener('click', onDocumentClick)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div ref="boxRef" class="notif">
    <button
      ref="bellRef"
      class="cy-btn cy-btn--icon cy-btn--ghost topbar__icon notif__bell"
      type="button"
      :aria-expanded="open"
      aria-haspopup="menu"
      aria-label="通知"
      title="通知"
      @click="toggle"
    >
      <AppIcon name="bell" :size="20" />
      <span v-if="unread > 0" class="notif__badge" aria-hidden="true">
        {{ unread > 99 ? '99+' : unread }}
      </span>
    </button>

    <Transition name="notif-panel">
      <div v-if="open" class="notif__panel" role="menu">
        <div class="notif__head">
          <span class="notif__title">通知</span>
          <button v-if="unread > 0" class="notif__all" type="button" @click="markAll">
            全部已读
          </button>
        </div>

        <p v-if="loading" class="notif__hint">加载中…</p>
        <p v-else-if="items.length === 0" class="notif__hint">暂无通知</p>

        <ul v-else class="notif__list">
          <li v-for="item in items" :key="item.id">
            <button
              class="notif__item"
              :class="{ 'is-unread': !item.is_read }"
              type="button"
              role="menuitem"
              @click="openItem(item)"
            >
              <UserAvatar
                :src="isSystemNotice(item.type) ? '' : item.avatar"
                :size="36"
                :alt="isSystemNotice(item.type) ? '系统通知' : nameOf(item)"
              />
              <span class="notif__body">
                <span class="notif__line">
                  <!-- 管理动作类通知不显示触发者（管理员账号对普通用户没有意义） -->
                  <span v-if="!isSystemNotice(item.type)" class="notif__name cy-clamp-1">
                    {{ nameOf(item) }}
                  </span>
                  <span class="notif__action">{{ actionText(item.type) }}</span>
                </span>
                <span v-if="item.content" class="notif__content cy-clamp-2">
                  {{ item.content }}
                </span>
                <span class="notif__meta">
                  <span class="notif__video cy-clamp-1">{{ item.video_title }}</span>
                  <span class="notif__time">{{ formatRelativeDate(item.created_at) }}</span>
                </span>
              </span>
              <span v-if="!item.is_read" class="notif__dot" aria-hidden="true" />
            </button>
          </li>
        </ul>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
/* 铃铛按钮 + 面板的定位容器 */
.notif {
  position: relative;
}

.notif__bell {
  position: relative;
}

/* 未读数角标：红底白字，压在铃铛右上角 */
.notif__badge {
  position: absolute;
  top: 4px;
  right: 4px;
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-danger);
  color: var(--cy-on-primary);
  font-size: 10px;
  line-height: 16px;
  font-weight: 600;
  text-align: center;
}

.notif__panel {
  position: absolute;
  top: calc(100% + var(--cy-space-2));
  right: 0;
  z-index: 120;
  width: 360px;
  max-height: 480px;
  overflow-y: auto;
  padding: var(--cy-space-2);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-lg);
  background: var(--cy-surface);
  box-shadow: var(--cy-shadow-pop);
}

.notif__head {
  position: sticky;
  top: calc(-1 * var(--cy-space-2));
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--cy-space-3);
  padding: var(--cy-space-2) var(--cy-space-3);
  background: var(--cy-surface);
}

.notif__title {
  font-size: 15px;
  line-height: 22px;
  font-weight: 600;
}

.notif__all {
  color: var(--cy-accent);
  font-size: 13px;
  line-height: 20px;
}

.notif__all:hover {
  text-decoration: underline;
}

.notif__hint {
  padding: var(--cy-space-5) var(--cy-space-3);
  color: var(--cy-text-muted);
  font-size: 13px;
  text-align: center;
}

.notif__list {
  display: flex;
  flex-direction: column;
}

.notif__item {
  position: relative;
  display: flex;
  gap: var(--cy-space-3);
  width: 100%;
  padding: var(--cy-space-3);
  border-radius: var(--cy-radius-sm);
  text-align: left;
  transition: background-color var(--cy-dur-fast) var(--cy-ease);
}

.notif__item:hover {
  background: var(--cy-surface-2);
}

/* 未读项用一层很浅的紫底提示，与已读区分 */
.notif__item.is-unread {
  background: var(--cy-primary-soft);
}

.notif__body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1;
}

.notif__line {
  display: flex;
  align-items: baseline;
  gap: var(--cy-space-1);
  min-width: 0;
}

.notif__name {
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
  font-weight: 600;
}

.notif__action {
  flex-shrink: 0;
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 20px;
}

.notif__content {
  color: var(--cy-text);
  font-size: 13px;
  line-height: 19px;
}

.notif__meta {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  min-width: 0;
  margin-top: 2px;
}

.notif__video {
  flex: 1;
  min-width: 0;
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
}

.notif__time {
  flex-shrink: 0;
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
}

/* 未读小圆点，压在条目右上角 */
.notif__dot {
  position: absolute;
  top: 10px;
  right: 10px;
  width: 8px;
  height: 8px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-primary);
}

/* 面板淡入，并从上方轻推下来（与账号下拉同一套手感） */
.notif-panel-enter-active,
.notif-panel-leave-active {
  transition: opacity var(--cy-dur-fast) var(--cy-ease),
    transform var(--cy-dur-fast) var(--cy-ease);
}

.notif-panel-enter-from,
.notif-panel-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}
</style>
