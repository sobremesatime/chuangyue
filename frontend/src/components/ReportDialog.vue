<script setup lang="ts">
// 举报弹框。
//
// 观看页里视频与评论的「举报」共用一个弹框，靠 targetType / targetId 区分对象：
// targetType=1 举报视频，=2 举报评论。表单只负责收集理由与补充说明，
// 提交动作在组件内完成（调 createReport），成功后 emit submitted，由父组件决定后续反馈。
//
// 可访问性机制沿用项目内其它弹框：内芯固定浅色（.cy-theme-light）、遮罩点击关闭、
// Escape 关闭、Tab 焦点圈定、打开时锁住页面滚动并重置表单、关闭后焦点还给唤出它的按钮。

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import { createReport } from '@/api/report'
import { toErrorMessage } from '@/api/request'
import AppIcon from '@/components/AppIcon.vue'
import { REPORT_REASONS, REPORT_TARGET_VIDEO } from '@/types/api'

const props = withDefaults(
  defineProps<{
    /** 是否打开 */
    open: boolean
    /** 被举报对象类型：1 视频、2 评论 */
    targetType: 1 | 2
    /** 被举报对象 id */
    targetId: string
    /** 外部提交态，与内部状态取或，便于父组件统一控制 */
    busy?: boolean
    /** 外部错误文案，与内部错误取或 */
    error?: string
  }>(),
  { busy: false, error: '' },
)

const emit = defineEmits<{ close: []; submitted: [] }>()

/** 补充说明上限，与后端约束一致 */
const DETAIL_MAX = 200

const dialogRef = ref<HTMLElement | null>(null)
/** 理由白名单容器，打开弹框后焦点落在第一个理由上 */
const reasonGroupRef = ref<HTMLElement | null>(null)

const reason = ref('')
const detail = ref('')
const submitting = ref(false)
const submitError = ref('')

const title = computed(() => (props.targetType === REPORT_TARGET_VIDEO ? '举报视频' : '举报评论'))
const isBusy = computed(() => props.busy || submitting.value)
const errorText = computed(() => props.error || submitError.value)
const canSubmit = computed(() => !!reason.value && !isBusy.value)

let lastActiveElement: HTMLElement | null = null
let scrollLocked = false

function lockScroll(): void {
  document.body.style.overflow = 'hidden'
  scrollLocked = true
}

function unlockScroll(): void {
  if (scrollLocked) {
    document.body.style.overflow = ''
    scrollLocked = false
  }
}

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
  // 焦点圈定在弹框内，Tab 到两端时回绕
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

/** 提交中不允许关闭，避免请求回来后状态对不上 */
function requestClose(): void {
  if (isBusy.value) {
    return
  }
  emit('close')
}

async function handleSubmit(): Promise<void> {
  if (!canSubmit.value) {
    return
  }
  submitting.value = true
  submitError.value = ''
  try {
    await createReport({
      target_type: props.targetType,
      target_id: props.targetId,
      reason: reason.value,
      // 补充说明为空时不提交，避免带上一个空串
      ...(detail.value.trim() ? { detail: detail.value.trim() } : {}),
    })
    emit('submitted')
  } catch (e) {
    // 失败时保留已选理由与输入，方便直接重试
    submitError.value = toErrorMessage(e)
  } finally {
    submitting.value = false
  }
}

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      lastActiveElement = document.activeElement as HTMLElement | null
      // 每次打开都重置，避免上一次的内容残留
      reason.value = ''
      detail.value = ''
      submitError.value = ''
      lockScroll()
      document.addEventListener('keydown', onKeydown, true)
      await nextTick()
      const firstReason = reasonGroupRef.value?.querySelector<HTMLElement>('button')
      ;(firstReason ?? dialogRef.value)?.focus()
      return
    }
    unlockScroll()
    document.removeEventListener('keydown', onKeydown, true)
    lastActiveElement?.focus?.()
    lastActiveElement = null
  },
)

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown, true)
  unlockScroll()
})
</script>

<template>
  <Teleport to="body">
    <Transition name="report-dialog">
      <div v-if="open" class="report-dialog" @click.self="requestClose">
        <div
          ref="dialogRef"
          class="cy-theme-light report-dialog__dialog"
          role="dialog"
          aria-modal="true"
          :aria-label="title"
          tabindex="-1"
        >
          <button
            class="report-dialog__close"
            type="button"
            aria-label="关闭"
            :disabled="isBusy"
            @click="requestClose"
          >
            <AppIcon name="close" :size="20" />
          </button>

          <h2 class="report-dialog__title">{{ title }}</h2>

          <form class="report-dialog__form" @submit.prevent="handleSubmit">
            <div class="cy-field">
              <span class="cy-label" id="report-reason-label">举报理由</span>
              <div
                ref="reasonGroupRef"
                class="report-dialog__reasons"
                role="radiogroup"
                aria-labelledby="report-reason-label"
              >
                <button
                  v-for="item in REPORT_REASONS"
                  :key="item"
                  class="report-dialog__reason"
                  :class="{ 'is-active': reason === item }"
                  type="button"
                  role="radio"
                  :aria-checked="reason === item"
                  @click="reason = item"
                >
                  {{ item }}
                </button>
              </div>
            </div>

            <div class="cy-field">
              <label class="cy-label" for="report-detail">补充说明（可选）</label>
              <textarea
                id="report-detail"
                v-model="detail"
                class="cy-input report-dialog__textarea"
                rows="3"
                :maxlength="DETAIL_MAX"
                placeholder="补充一些有助于核实的信息…"
              />
              <p class="report-dialog__count cy-num">{{ detail.length }}/{{ DETAIL_MAX }}</p>
            </div>

            <p v-if="errorText" class="cy-alert cy-alert--error report-dialog__error">
              {{ errorText }}
            </p>

            <div class="report-dialog__actions">
              <button
                class="cy-btn cy-btn--outline"
                type="button"
                :disabled="isBusy"
                @click="requestClose"
              >
                取消
              </button>
              <button class="cy-btn cy-btn--primary" type="submit" :disabled="!canSubmit">
                {{ isBusy ? '提交中…' : '提交举报' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
/* 遮罩铺满视口；用 flex + margin:auto 居中，弹框高于视口时可滚动而不裁掉顶部 */
.report-dialog {
  position: fixed;
  inset: 0;
  z-index: 200;
  display: flex;
  padding: var(--cy-space-5);
  background: var(--cy-overlay);
  overflow-y: auto;
}

.report-dialog__dialog {
  position: relative;
  margin: auto;
  width: 100%;
  max-width: 440px;
  padding: var(--cy-space-6) var(--cy-space-6) var(--cy-space-5);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-lg);
  background: var(--cy-surface);
  box-shadow: var(--cy-shadow-pop);
}

.report-dialog__close {
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

.report-dialog__close:hover {
  background: var(--cy-surface-2);
  color: var(--cy-text);
}

.report-dialog__title {
  text-align: center;
  font-size: 17px;
  line-height: 24px;
  font-weight: 600;
  color: var(--cy-text);
}

.report-dialog__form {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-5);
  margin-top: var(--cy-space-5);
}

.report-dialog__reasons {
  display: flex;
  flex-wrap: wrap;
  gap: var(--cy-space-2);
}

/* 理由是单选表单控件，选中态用星紫描边 + 浅紫底，与筛选 pill 区分 */
.report-dialog__reason {
  height: 36px;
  padding: 0 var(--cy-space-4);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-full);
  background: var(--cy-surface-2);
  color: var(--cy-text-secondary);
  font-size: 14px;
  transition: background-color var(--cy-dur-fast) var(--cy-ease),
    border-color var(--cy-dur-fast) var(--cy-ease), color var(--cy-dur-fast) var(--cy-ease);
}

.report-dialog__reason:hover {
  color: var(--cy-text);
}

.report-dialog__reason.is-active {
  border-color: var(--cy-primary);
  background: var(--cy-primary-soft);
  color: var(--cy-primary-strong);
  font-weight: 600;
}

/* 多行输入按内容撑高，字数在右下角 */
.report-dialog__textarea {
  height: auto;
  padding: 10px 14px;
  line-height: 20px;
  resize: none;
}

.report-dialog__count {
  align-self: flex-end;
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
}

.report-dialog__error {
  margin: 0;
}

.report-dialog__actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--cy-space-3);
}

/* 弹框淡入 + 轻微收放 */
.report-dialog-enter-active,
.report-dialog-leave-active {
  transition: opacity var(--cy-dur-base) var(--cy-ease);
}

.report-dialog-enter-active .report-dialog__dialog,
.report-dialog-leave-active .report-dialog__dialog {
  transition: transform var(--cy-dur-base) var(--cy-ease);
}

.report-dialog-enter-from,
.report-dialog-leave-to {
  opacity: 0;
}

.report-dialog-enter-from .report-dialog__dialog,
.report-dialog-leave-to .report-dialog__dialog {
  transform: scale(0.96);
}
</style>