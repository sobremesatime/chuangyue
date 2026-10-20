<script setup lang="ts">
// 危险操作确认弹框。
//
// 目前用于管理后台的「删除视频」：删除不可恢复，必须让用户再确认一次。
// 交互约定与其它弹框一致（内芯固定浅色、Escape 关闭、Tab 焦点圈定、
// 打开时锁页面滚动），但默认焦点落在「取消」上——避免回车误触发破坏性操作。

import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

import AppIcon from '@/components/AppIcon.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    description?: string
    confirmLabel?: string
    /** 确认按钮是否用危险色（删除类操作为 true） */
    danger?: boolean
    /** 执行中：禁用按钮并屏蔽关闭 */
    busy?: boolean
    /** 服务端返回的错误，展示在按钮上方 */
    error?: string
  }>(),
  {
    description: '',
    confirmLabel: '确认',
    danger: false,
    busy: false,
    error: '',
  },
)

const emit = defineEmits<{ close: []; confirm: [] }>()

const dialogRef = ref<HTMLElement | null>(null)
const cancelRef = ref<HTMLButtonElement | null>(null)

let lastActiveElement: HTMLElement | null = null
let scrollLocked = false

function focusableItems(): HTMLElement[] {
  if (!dialogRef.value) {
    return []
  }
  return Array.from(
    dialogRef.value.querySelectorAll<HTMLElement>('button:not([disabled]), [tabindex]:not([tabindex="-1"])'),
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

function requestClose(): void {
  if (props.busy) {
    return
  }
  emit('close')
}

function handleConfirm(): void {
  if (props.busy) {
    return
  }
  emit('confirm')
}

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      lastActiveElement = document.activeElement as HTMLElement | null
      document.body.style.overflow = 'hidden'
      scrollLocked = true
      document.addEventListener('keydown', onKeydown, true)
      await nextTick()
      cancelRef.value?.focus()
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
    <Transition name="confirm">
      <div v-if="open" class="confirm" @click.self="requestClose">
        <div
          ref="dialogRef"
          class="cy-theme-light confirm__dialog"
          role="alertdialog"
          aria-modal="true"
          :aria-label="title"
          tabindex="-1"
        >
          <button
            class="confirm__close"
            type="button"
            aria-label="关闭"
            :disabled="busy"
            @click="requestClose"
          >
            <AppIcon name="close" :size="20" />
          </button>

          <h2 class="confirm__title">{{ title }}</h2>
          <p v-if="description" class="confirm__desc">{{ description }}</p>

          <p v-if="error" class="cy-alert cy-alert--error confirm__error">{{ error }}</p>

          <div class="confirm__actions">
            <button
              ref="cancelRef"
              class="cy-btn cy-btn--outline"
              type="button"
              :disabled="busy"
              @click="requestClose"
            >
              取消
            </button>
            <button
              class="cy-btn"
              :class="danger ? 'cy-btn--danger' : 'cy-btn--primary'"
              type="button"
              :disabled="busy"
              @click="handleConfirm"
            >
              {{ busy ? '处理中…' : confirmLabel }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.confirm {
  position: fixed;
  inset: 0;
  z-index: 210;
  display: flex;
  padding: var(--cy-space-5);
  background: var(--cy-overlay);
  overflow-y: auto;
}

.confirm__dialog {
  position: relative;
  margin: auto;
  width: 100%;
  max-width: 400px;
  padding: var(--cy-space-6) var(--cy-space-6) var(--cy-space-5);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-lg);
  background: var(--cy-surface);
  box-shadow: var(--cy-shadow-pop);
}

.confirm__close {
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

.confirm__close:hover {
  background: var(--cy-surface-2);
  color: var(--cy-text);
}

/* 标题与说明居中，两侧按钮等宽铺满弹框（参考 B 站删除弹框） */
.confirm__title {
  margin-top: var(--cy-space-5);
  font-size: 17px;
  line-height: 24px;
  font-weight: 600;
  text-align: center;
  color: var(--cy-text);
}

.confirm__desc {
  margin-top: var(--cy-space-2);
  color: var(--cy-text-secondary);
  font-size: 14px;
  line-height: 22px;
  text-align: center;
  overflow-wrap: anywhere;
}

.confirm__error {
  margin-top: var(--cy-space-4);
}

.confirm__actions {
  display: flex;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-5);
}

.confirm__actions > * {
  flex: 1;
}

.confirm-enter-active,
.confirm-leave-active {
  transition: opacity var(--cy-dur-base) var(--cy-ease);
}

.confirm-enter-active .confirm__dialog,
.confirm-leave-active .confirm__dialog {
  transition: transform var(--cy-dur-base) var(--cy-ease);
}

.confirm-enter-from,
.confirm-leave-to {
  opacity: 0;
}

.confirm-enter-from .confirm__dialog,
.confirm-leave-to .confirm__dialog {
  transform: scale(0.96);
}
</style>
