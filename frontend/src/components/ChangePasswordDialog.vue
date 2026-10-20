<script setup lang="ts">
// 修改密码弹框：当前密码 / 新密码 / 确认新密码。
//
// 与 ProfileFieldDialog 共用同一套弹框外壳（浅色内芯、Escape 关闭、Tab 焦点圈定、
// 锁滚动、关闭后归还焦点），差别只在内容：单项编辑弹框一次只收一个值，
// 而改密必须同时拿到当前密码与新密码，所以单独成一个组件。
//
// 校验先在前端拦一遍（非空、6~64 个字符、两次一致），服务端返回的原因由 error 传入内联展示。

import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

import AppIcon from '@/components/AppIcon.vue'

/** 与注册时的密码口径保持一致 */
const PASSWORD_MIN_LENGTH = 6
const PASSWORD_MAX_LENGTH = 64

const props = withDefaults(
  defineProps<{
    /** 是否打开 */
    open: boolean
    /** 保存中：禁用按钮并屏蔽关闭 */
    saving?: boolean
    /** 服务端校验失败的原因，如「当前密码不正确」 */
    error?: string
  }>(),
  { saving: false, error: '' },
)

const emit = defineEmits<{
  close: []
  submit: [payload: { oldPassword: string; newPassword: string }]
}>()

const dialogRef = ref<HTMLElement | null>(null)
/** 弹框打开后把焦点落在「当前密码」上 */
const oldFieldRef = ref<HTMLInputElement | null>(null)

// 密码不做 trim：空格也是密码的合法字符，只在提交时提示非空
const oldPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const showOld = ref(false)
const showNew = ref(false)
const showConfirm = ref(false)

const emptyFieldErrors = () => ({ oldPassword: '', newPassword: '', confirmPassword: '' })
const fieldErrors = ref(emptyFieldErrors())

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
      'button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
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

/** 保存中不允许关闭，避免请求回来后状态对不上 */
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

  const errors = emptyFieldErrors()
  if (!oldPassword.value) {
    errors.oldPassword = '请输入当前密码'
  }
  if (!newPassword.value) {
    errors.newPassword = '请输入新密码'
  } else if (
    newPassword.value.length < PASSWORD_MIN_LENGTH ||
    newPassword.value.length > PASSWORD_MAX_LENGTH
  ) {
    errors.newPassword = `密码需为 ${PASSWORD_MIN_LENGTH}~${PASSWORD_MAX_LENGTH} 个字符`
  }
  if (!confirmPassword.value) {
    errors.confirmPassword = '请再次输入新密码'
  } else if (newPassword.value !== confirmPassword.value) {
    errors.confirmPassword = '两次输入的密码不一致'
  }
  fieldErrors.value = errors
  if (Object.values(errors).some((msg) => msg !== '')) {
    return
  }

  emit('submit', { oldPassword: oldPassword.value, newPassword: newPassword.value })
}

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      lastActiveElement = document.activeElement as HTMLElement | null
      oldPassword.value = ''
      newPassword.value = ''
      confirmPassword.value = ''
      showOld.value = false
      showNew.value = false
      showConfirm.value = false
      fieldErrors.value = emptyFieldErrors()
      lockScroll()
      // 捕获阶段监听：先于顶栏的同名监听处理 Escape
      document.addEventListener('keydown', onKeydown, true)
      await nextTick()
      oldFieldRef.value?.focus()
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
    <Transition name="field-dialog">
      <div v-if="open" class="field-dialog" @click.self="requestClose">
        <div
          ref="dialogRef"
          class="cy-theme-light field-dialog__dialog"
          role="dialog"
          aria-modal="true"
          aria-label="修改密码"
          tabindex="-1"
        >
          <button
            class="field-dialog__close"
            type="button"
            aria-label="关闭"
            :disabled="saving"
            @click="requestClose"
          >
            <AppIcon name="close" :size="20" />
          </button>

          <h2 class="field-dialog__title">修改密码</h2>

          <form class="field-dialog__form" @submit.prevent="handleSubmit">
            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': fieldErrors.oldPassword }"
                for="change-password-old"
              >
                当前密码
              </label>
              <div class="cy-input-wrap">
                <input
                  id="change-password-old"
                  ref="oldFieldRef"
                  v-model="oldPassword"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': fieldErrors.oldPassword }"
                  :type="showOld ? 'text' : 'password'"
                  autocomplete="current-password"
                  placeholder="请输入当前密码"
                />
                <button
                  class="cy-input-wrap__action"
                  type="button"
                  :aria-label="showOld ? '隐藏密码' : '显示密码'"
                  @click="showOld = !showOld"
                >
                  <AppIcon :name="showOld ? 'eye-off' : 'eye'" :size="18" />
                </button>
              </div>
              <p v-if="fieldErrors.oldPassword" class="cy-field__error">
                {{ fieldErrors.oldPassword }}
              </p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': fieldErrors.newPassword }"
                for="change-password-new"
              >
                新密码
              </label>
              <div class="cy-input-wrap">
                <input
                  id="change-password-new"
                  v-model="newPassword"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': fieldErrors.newPassword }"
                  :type="showNew ? 'text' : 'password'"
                  autocomplete="new-password"
                  placeholder="请输入新密码"
                />
                <button
                  class="cy-input-wrap__action"
                  type="button"
                  :aria-label="showNew ? '隐藏密码' : '显示密码'"
                  @click="showNew = !showNew"
                >
                  <AppIcon :name="showNew ? 'eye-off' : 'eye'" :size="18" />
                </button>
              </div>
              <p v-if="fieldErrors.newPassword" class="cy-field__error">
                {{ fieldErrors.newPassword }}
              </p>
              <p v-else class="cy-field__hint">
                {{ PASSWORD_MIN_LENGTH }}~{{ PASSWORD_MAX_LENGTH }} 个字符
              </p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': fieldErrors.confirmPassword }"
                for="change-password-confirm"
              >
                确认新密码
              </label>
              <div class="cy-input-wrap">
                <input
                  id="change-password-confirm"
                  v-model="confirmPassword"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': fieldErrors.confirmPassword }"
                  :type="showConfirm ? 'text' : 'password'"
                  autocomplete="new-password"
                  placeholder="请再次输入新密码"
                />
                <button
                  class="cy-input-wrap__action"
                  type="button"
                  :aria-label="showConfirm ? '隐藏密码' : '显示密码'"
                  @click="showConfirm = !showConfirm"
                >
                  <AppIcon :name="showConfirm ? 'eye-off' : 'eye'" :size="18" />
                </button>
              </div>
              <p v-if="fieldErrors.confirmPassword" class="cy-field__error">
                {{ fieldErrors.confirmPassword }}
              </p>
            </div>

            <p v-if="error" class="cy-alert cy-alert--error">{{ error }}</p>

            <div class="field-dialog__actions">
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
/* 遮罩铺满视口；用 flex + margin:auto 居中，弹框高于视口时可滚动而不裁掉顶部 */
.field-dialog {
  position: fixed;
  inset: 0;
  z-index: 200;
  display: flex;
  padding: var(--cy-space-5);
  background: var(--cy-overlay);
  overflow-y: auto;
}

.field-dialog__dialog {
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

.field-dialog__close {
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

.field-dialog__close:hover {
  background: var(--cy-surface-2);
  color: var(--cy-text);
}

.field-dialog__title {
  font-size: 17px;
  line-height: 24px;
  font-weight: 600;
  color: var(--cy-text);
}

.field-dialog__form {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-5);
  margin-top: var(--cy-space-5);
}

.field-dialog__actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--cy-space-3);
}

/* 浅色底上的紫色文字用深一档的紫，标签出错时切成危险色 */
.cy-label.is-invalid {
  color: var(--cy-danger);
}

/* 弹框淡入 + 轻微收放 */
.field-dialog-enter-active,
.field-dialog-leave-active {
  transition: opacity var(--cy-dur-base) var(--cy-ease);
}

.field-dialog-enter-active .field-dialog__dialog,
.field-dialog-leave-active .field-dialog__dialog {
  transition: transform var(--cy-dur-base) var(--cy-ease);
}

.field-dialog-enter-from,
.field-dialog-leave-to {
  opacity: 0;
}

.field-dialog-enter-from .field-dialog__dialog,
.field-dialog-leave-to .field-dialog__dialog {
  transform: scale(0.96);
}
</style>
