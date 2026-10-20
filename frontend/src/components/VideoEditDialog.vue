<script setup lang="ts">
// 视频编辑弹框（管理后台用）。
//
// 可改：标题、简介、分类、封面。片源不可替换——换片源等于换内容，
// 那种场景应该走「删除 + 重新投稿」。
//
// 弹框只负责「渲染 + 收集输入」，请求与错误提示交给父组件，
// 与 ProfileFieldDialog 保持同一套交互约定：内芯固定浅色、
// Escape 关闭、Tab 焦点圈定、打开时锁页面滚动、保存中禁止关闭。

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import type { UpdateVideoPayload } from '@/api/video'
import { VIDEO_CATEGORIES, type Video } from '@/types/api'

/** 与后端 coverExts、契约 maxLength 保持一致 */
const COVER_ACCEPT = 'image/png,image/jpeg,image/webp'
const COVER_EXTS = ['.jpg', '.jpeg', '.png', '.webp']
const MAX_TITLE = 128
const MAX_DESCRIPTION = 512

const props = withDefaults(
  defineProps<{
    open: boolean
    /** 被编辑的视频，null 表示没有选中项 */
    video: Video | null
    /** 提交中：禁用按钮并屏蔽关闭 */
    saving?: boolean
    /** 服务端返回的错误，展示在按钮上方 */
    error?: string
  }>(),
  { saving: false, error: '' },
)

const emit = defineEmits<{ close: []; submit: [payload: UpdateVideoPayload] }>()

const dialogRef = ref<HTMLElement | null>(null)
const titleRef = ref<HTMLInputElement | null>(null)
const coverInputRef = ref<HTMLInputElement | null>(null)

const title = ref('')
const description = ref('')
/** 0 表示未分类 */
const categoryId = ref(0)

const coverFile = ref<File | null>(null)
/** 新选封面的本地预览地址，切换 / 卸载时需要 revoke */
const coverPreview = ref('')
const localError = ref('')

/** 预览图：选了新封面看新的，否则看原有封面 */
const previewSrc = computed(() => coverPreview.value || props.video?.cover_url || '')
/** 校验失败优先显示本地提示，其次才是服务端错误 */
const shownError = computed(() => localError.value || props.error)

const titleCount = computed(() => title.value.length)
const descriptionCount = computed(() => description.value.length)

let lastActiveElement: HTMLElement | null = null
let scrollLocked = false

function extensionOf(name: string): string {
  const index = name.lastIndexOf('.')
  return index >= 0 ? name.slice(index).toLowerCase() : ''
}

function focusableItems(): HTMLElement[] {
  if (!dialogRef.value) {
    return []
  }
  return Array.from(
    dialogRef.value.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
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

function releaseCoverPreview(): void {
  if (coverPreview.value) {
    URL.revokeObjectURL(coverPreview.value)
    coverPreview.value = ''
  }
}

function acceptCover(file: File): void {
  localError.value = ''
  if (!COVER_EXTS.includes(extensionOf(file.name))) {
    localError.value = `封面仅支持 ${COVER_EXTS.join(' / ')} 格式`
    return
  }
  releaseCoverPreview()
  coverFile.value = file
  coverPreview.value = URL.createObjectURL(file)
}

function onCoverChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // 清空 value，否则再次选择同一张图不会触发 change
  input.value = ''
  if (file) {
    acceptCover(file)
  }
}

function pickCover(): void {
  coverInputRef.value?.click()
}

/** 撤销新选的封面，回到原有封面 */
function resetCover(): void {
  releaseCoverPreview()
  coverFile.value = null
}

function handleSubmit(): void {
  if (props.saving) {
    return
  }
  const trimmedTitle = title.value.trim()
  if (!trimmedTitle) {
    localError.value = '请填写视频标题'
    return
  }

  localError.value = ''
  emit('submit', {
    title: trimmedTitle,
    description: description.value.trim(),
    category_id: categoryId.value,
    cover: coverFile.value ?? undefined,
  })
}

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      lastActiveElement = document.activeElement as HTMLElement | null
      // 每次打开都用当前视频重置草稿，避免残留上一次的输入
      title.value = props.video?.title ?? ''
      description.value = props.video?.description ?? ''
      categoryId.value = props.video?.category_id ?? 0
      resetCover()
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
    releaseCoverPreview()
    coverFile.value = null
    lastActiveElement?.focus?.()
    lastActiveElement = null
  },
)

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown, true)
  if (scrollLocked) {
    document.body.style.overflow = ''
  }
  releaseCoverPreview()
})
</script>

<template>
  <Teleport to="body">
    <Transition name="video-edit">
      <div v-if="open" class="video-edit" @click.self="requestClose">
        <div
          ref="dialogRef"
          class="cy-theme-light video-edit__dialog"
          role="dialog"
          aria-modal="true"
          aria-label="编辑视频"
          tabindex="-1"
        >
          <button
            class="video-edit__close"
            type="button"
            aria-label="关闭"
            :disabled="saving"
            @click="requestClose"
          >
            <AppIcon name="close" :size="20" />
          </button>

          <h2 class="video-edit__title">编辑视频</h2>

          <form class="video-edit__form" @submit.prevent="handleSubmit">
            <div class="cy-field">
              <label class="cy-label" for="video-edit-title">标题</label>
              <input
                id="video-edit-title"
                ref="titleRef"
                v-model="title"
                class="cy-input"
                type="text"
                placeholder="给这段视频起个名字"
                :maxlength="MAX_TITLE"
                :disabled="saving"
              />
              <p class="video-edit__count cy-caption cy-num">{{ titleCount }}/{{ MAX_TITLE }}</p>
            </div>

            <div class="cy-field">
              <label class="cy-label" for="video-edit-description">简介</label>
              <textarea
                id="video-edit-description"
                v-model="description"
                class="cy-input video-edit__textarea"
                rows="3"
                placeholder="简单介绍这段内容，留空即清空简介"
                :maxlength="MAX_DESCRIPTION"
                :disabled="saving"
              />
              <p class="video-edit__count cy-caption cy-num">
                {{ descriptionCount }}/{{ MAX_DESCRIPTION }}
              </p>
            </div>

            <div class="cy-field">
              <label class="cy-label" for="video-edit-category">分类</label>
              <div class="video-edit__select">
                <select
                  id="video-edit-category"
                  v-model.number="categoryId"
                  class="cy-input video-edit__select-input"
                  :disabled="saving"
                >
                  <option :value="0">未分类</option>
                  <option v-for="category in VIDEO_CATEGORIES" :key="category.id" :value="category.id">
                    {{ category.name }}
                  </option>
                </select>
                <AppIcon class="video-edit__select-arrow" name="chevron-down" :size="18" />
              </div>
            </div>

            <div class="cy-field">
              <span class="cy-label">封面</span>
              <div class="video-edit__cover">
                <div class="video-edit__cover-preview">
                  <img v-if="previewSrc" :src="previewSrc" alt="封面预览" />
                  <span v-else class="video-edit__cover-empty" aria-hidden="true">
                    <AppIcon name="eye" :size="20" />
                  </span>
                </div>

                <div class="video-edit__cover-actions">
                  <input
                    ref="coverInputRef"
                    class="cy-sr-only"
                    type="file"
                    :accept="COVER_ACCEPT"
                    @change="onCoverChange"
                  />
                  <button
                    class="cy-btn cy-btn--secondary cy-btn--sm"
                    type="button"
                    :disabled="saving"
                    @click="pickCover"
                  >
                    更换封面
                  </button>
                  <button
                    v-if="coverFile"
                    class="cy-btn cy-btn--ghost cy-btn--sm"
                    type="button"
                    :disabled="saving"
                    @click="resetCover"
                  >
                    撤销
                  </button>
                  <p class="video-edit__cover-hint cy-caption">
                    不更换则保留原封面，支持 {{ COVER_EXTS.join(' / ') }}
                  </p>
                </div>
              </div>
            </div>

            <p v-if="shownError" class="cy-alert cy-alert--error">{{ shownError }}</p>

            <div class="video-edit__actions">
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
.video-edit {
  position: fixed;
  inset: 0;
  z-index: 200;
  display: flex;
  padding: var(--cy-space-5);
  background: var(--cy-overlay);
  overflow-y: auto;
}

.video-edit__dialog {
  position: relative;
  margin: auto;
  width: 100%;
  max-width: 520px;
  padding: var(--cy-space-6) var(--cy-space-6) var(--cy-space-5);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-lg);
  background: var(--cy-surface);
  box-shadow: var(--cy-shadow-pop);
}

.video-edit__close {
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

.video-edit__close:hover {
  background: var(--cy-surface-2);
  color: var(--cy-text);
}

.video-edit__title {
  font-size: 17px;
  line-height: 24px;
  font-weight: 600;
  color: var(--cy-text);
}

.video-edit__form {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-4);
  margin-top: var(--cy-space-5);
}

.video-edit__count {
  align-self: flex-end;
}

.video-edit__textarea {
  height: auto;
  padding: 10px 14px;
  line-height: 20px;
  resize: vertical;
}

/* 原生下拉按 .cy-input 统一外观，箭头自己画一个 */
.video-edit__select {
  position: relative;
}

.video-edit__select-input {
  appearance: none;
  padding-right: 44px;
}

.video-edit__select-arrow {
  position: absolute;
  top: 50%;
  right: 14px;
  transform: translateY(-50%);
  color: var(--cy-text-muted);
  pointer-events: none;
}

.video-edit__cover {
  display: flex;
  align-items: center;
  gap: var(--cy-space-4);
}

.video-edit__cover-preview {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 160px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-md);
  overflow: hidden;
  background: var(--cy-surface-2);
}

.video-edit__cover-preview img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.video-edit__cover-empty {
  color: var(--cy-text-muted);
}

.video-edit__cover-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--cy-space-2);
}

.video-edit__cover-hint {
  flex-basis: 100%;
}

.video-edit__actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--cy-space-3);
}

/* 弹框淡入 + 轻微收放 */
.video-edit-enter-active,
.video-edit-leave-active {
  transition: opacity var(--cy-dur-base) var(--cy-ease);
}

.video-edit-enter-active .video-edit__dialog,
.video-edit-leave-active .video-edit__dialog {
  transition: transform var(--cy-dur-base) var(--cy-ease);
}

.video-edit-enter-from,
.video-edit-leave-to {
  opacity: 0;
}

.video-edit-enter-from .video-edit__dialog,
.video-edit-leave-to .video-edit__dialog {
  transform: scale(0.96);
}

/* 窄屏：封面预览与按钮改为上下排，避免横向挤爆 */
@media (max-width: 599px) {
  .video-edit__cover {
    flex-direction: column;
    align-items: flex-start;
  }

  .video-edit__cover-preview {
    width: 100%;
  }
}
</style>
