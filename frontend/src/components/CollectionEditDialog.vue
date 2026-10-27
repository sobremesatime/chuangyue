<script setup lang="ts">
// 合集编辑弹框（管理后台用）。
//
// 可改：标题、简介、封面地址。合集与视频不同，封面走 URL 而不是上传文件，
// 因此这里是一个普通文本输入，不涉及 multipart。
//
// 弹框只负责「渲染 + 收集输入」，请求与错误提示交给父组件，
// 与 VideoEditDialog 保持同一套交互约定：内芯固定浅色、Escape 关闭、
// Tab 焦点圈定、打开时锁页面滚动、保存中禁止关闭。

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import type { AdminCollection, CreateCollectionPayload } from '@/types/api'

const props = withDefaults(
  defineProps<{
    open: boolean
    /** 被编辑的合集，null 表示新建 */
    collection?: AdminCollection | null
    /** 提交中：禁用按钮并屏蔽关闭 */
    saving?: boolean
    /** 服务端返回的错误，展示在按钮上方 */
    error?: string
  }>(),
  { collection: null, saving: false, error: '' },
)

const emit = defineEmits<{ close: []; submit: [payload: CreateCollectionPayload] }>()

const dialogRef = ref<HTMLElement | null>(null)
const titleRef = ref<HTMLInputElement | null>(null)

const title = ref('')
const description = ref('')
const coverUrl = ref('')
const localError = ref('')

/** 校验失败优先显示本地提示，其次才是服务端错误 */
const shownError = computed(() => localError.value || props.error)

let lastActiveElement: HTMLElement | null = null
let scrollLocked = false

function focusableItems(): HTMLElement[] {
  if (!dialogRef.value) {
    return []
  }
  return Array.from(
    dialogRef.value.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((el) => el.offsetWidth > 0 || el.offsetHeight > 0)
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    requestClose()
    return
  }
  if (event.key !== 'Tab') {
    return
  }
  const items = focusableItems()
  if (items.length === 0) {
    return
  }
  const first = items[0]
  const last = items[items.length - 1]
  const active = document.activeElement as HTMLElement | null

  if (event.shiftKey && (active === first || !dialogRef.value?.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus()
  }
}

/** 保存中不允许关闭，避免请求回来后列表状态对不上 */
function requestClose(): void {
  if (props.saving) {
    return
  }
  emit('close')
}

function handleSubmit(): void {
  if (props.saving) {
    return
  }
  const trimmedTitle = title.value.trim()
  if (!trimmedTitle) {
    localError.value = '请填写合集标题'
    return
  }

  localError.value = ''
  emit('submit', {
    title: trimmedTitle,
    description: description.value.trim(),
    cover_url: coverUrl.value.trim(),
  })
}

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      lastActiveElement = document.activeElement as HTMLElement | null
      // 每次打开都用当前合集重置草稿，避免残留上一次的输入
      title.value = props.collection?.title ?? ''
      description.value = props.collection?.description ?? ''
      coverUrl.value = props.collection?.cover_url ?? ''
      localError.value = ''

      document.body.style.overflow = 'hidden'
      scrollLocked = true
      document.addEventListener('keydown', onKeydown, true)

      await nextTick()
      titleRef.value?.focus()
      return
    }

    if (scrollLocked) {
      document.body.style.overflow = ''
      scrollLocked = false
    }
    document.removeEventListener('keydown', onKeydown, true)
    lastActiveElement?.focus?.()
    lastActiveElement = null
  },
)

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown, true)
  if (scrollLocked) {
    document.body.style.overflow = ''
  }
})
</script>

<template>
  <Teleport to="body">
    <Transition name="collection-edit">
      <div v-if="open" class="collection-edit" @click.self="requestClose">
        <div
          ref="dialogRef"
          class="cy-theme-light collection-edit__dialog"
          role="dialog"
          aria-modal="true"
          :aria-label="collection ? '编辑合集' : '新建合集'"
          tabindex="-1"
        >
          <button
            class="collection-edit__close"
            type="button"
            aria-label="关闭"
            :disabled="saving"
            @click="requestClose"
          >
            <AppIcon name="close" :size="20" />
          </button>

          <h2 class="collection-edit__title">{{ collection ? '编辑合集' : '新建合集' }}</h2>

          <form class="collection-edit__form" @submit.prevent="handleSubmit">
            <div class="cy-field">
              <label class="cy-label" for="collection-edit-title">标题</label>
              <input
                id="collection-edit-title"
                ref="titleRef"
                v-model="title"
                class="cy-input"
                type="text"
                placeholder="给这个合集起个名字"
                :disabled="saving"
              />
            </div>

            <div class="cy-field">
              <label class="cy-label" for="collection-edit-description">简介（可选）</label>
              <textarea
                id="collection-edit-description"
                v-model="description"
                class="cy-input collection-edit__textarea"
                rows="3"
                placeholder="简单介绍这个合集的内容，留空即不展示简介"
                :disabled="saving"
              />
            </div>

            <div class="cy-field">
              <label class="cy-label" for="collection-edit-cover">封面地址（可选）</label>
              <input
                id="collection-edit-cover"
                v-model="coverUrl"
                class="cy-input"
                type="text"
                placeholder="https://example.com/cover.jpg"
                :disabled="saving"
              />
              <p class="cy-field__hint">填写公开可访问的图片地址，留空则显示标题首字。</p>
            </div>

            <p v-if="shownError" class="cy-alert cy-alert--error">{{ shownError }}</p>

            <div class="collection-edit__actions">
              <button
                class="cy-btn cy-btn--outline"
                type="button"
                :disabled="saving"
                @click="requestClose"
              >
                取消
              </button>
              <button class="cy-btn cy-btn--primary" type="submit" :disabled="saving">
                {{ saving ? '保存中…' : '保存' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.collection-edit {
  position: fixed;
  inset: 0;
  z-index: 200;
  display: flex;
  padding: var(--cy-space-5);
  background: var(--cy-overlay);
  overflow-y: auto;
}

.collection-edit__dialog {
  position: relative;
  margin: auto;
  width: 100%;
  max-width: 480px;
  padding: var(--cy-space-6) var(--cy-space-6) var(--cy-space-5);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-lg);
  background: var(--cy-surface);
  box-shadow: var(--cy-shadow-pop);
}

.collection-edit__close {
  position: absolute;
  top: var(--cy-space-3);
  right: var(--cy-space-3);
  display: grid;
  place-items: center;
  width: 40px;
  height: 40px;
  border-radius: var(--cy-radius-full);
  color: var(--cy-text-muted);
  transition: background-color var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease);
}

.collection-edit__close:hover {
  background: var(--cy-surface-2);
  color: var(--cy-text);
}

.collection-edit__title {
  font-size: 17px;
  line-height: 24px;
  font-weight: 600;
  color: var(--cy-text);
}

.collection-edit__form {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-4);
  margin-top: var(--cy-space-5);
}

.collection-edit__textarea {
  height: auto;
  padding: 10px 14px;
  line-height: 20px;
  resize: vertical;
}

.collection-edit__actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--cy-space-3);
}

/* 弹框淡入 + 轻微收放 */
.collection-edit-enter-active,
.collection-edit-leave-active {
  transition: opacity var(--cy-dur-base) var(--cy-ease);
}

.collection-edit-enter-active .collection-edit__dialog,
.collection-edit-leave-active .collection-edit__dialog {
  transition: transform var(--cy-dur-base) var(--cy-ease);
}

.collection-edit-enter-from,
.collection-edit-leave-to {
  opacity: 0;
}

.collection-edit-enter-from .collection-edit__dialog,
.collection-edit-leave-to .collection-edit__dialog {
  transform: scale(0.96);
}
</style>
