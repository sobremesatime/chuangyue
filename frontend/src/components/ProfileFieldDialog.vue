<script setup lang="ts">
// 个人资料单项编辑弹框。
//
// 个人中心的「个人信息」卡里，每一行点开都是这个弹框，靠 type 决定内芯长什么样：
// text / textarea / date / radio。弹框只负责「渲染 + 收集输入」，取值与提交交给父组件，
// 因此它不关心业务字段叫什么，纯展示型组件。
//
// 内芯固定为浅色（.cy-theme-light）：与登录弹框一致，无论站点是深色还是浅色模式，
// 弹框都是月白卡片 + 文字墨，两种模式下观感统一。
//
// 可访问性：role="dialog" + aria-modal、Escape 关闭、Tab 焦点圈定在弹框内、
// 打开时锁住页面滚动、关闭后把焦点还给唤出它的那一行。

import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

import AppIcon from '@/components/AppIcon.vue'

/** 单选类字段的候选项 */
export interface FieldOption {
  label: string
  value: string
}

const props = withDefaults(
  defineProps<{
    /** 是否打开 */
    open: boolean
    /** 弹框标题，如「修改昵称」 */
    title: string
    /** 字段名，作为输入框标签 */
    label: string
    /** 内芯形态 */
    type: 'text' | 'textarea' | 'date' | 'radio'
    /** 当前值，性别这类数字字段由父组件转成字符串传入 */
    value: string
    /** type 为 radio 时的候选项 */
    options?: FieldOption[]
    placeholder?: string
    hint?: string
    /** 最大输入长度，0 表示不限制 */
    maxlength?: number
    /** 保存中：禁用按钮并屏蔽关闭 */
    saving?: boolean
    /** 服务端校验失败的原因，挂在字段下方 */
    error?: string
  }>(),
  {
    options: () => [],
    placeholder: '',
    hint: '',
    maxlength: 0,
    saving: false,
    error: '',
  },
)

const emit = defineEmits<{ close: []; submit: [value: string] }>()

const dialogRef = ref<HTMLElement | null>(null)
/** text / textarea / date 的输入框，打开弹框后把焦点落在这里 */
const fieldRef = ref<HTMLInputElement | HTMLTextAreaElement | null>(null)
/** 草稿值：编辑期间只改这里，点保存才交出去 */
const draft = ref('')

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
  emit('submit', draft.value)
}

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      lastActiveElement = document.activeElement as HTMLElement | null
      draft.value = props.value
      lockScroll()
      // 捕获阶段监听：先于顶栏的同名监听处理 Escape
      document.addEventListener('keydown', onKeydown, true)
      await nextTick()
      // 单选类没有输入框，兜底把焦点落在弹框本身
      ;(fieldRef.value ?? dialogRef.value)?.focus()
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
          :aria-label="title"
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

          <h2 class="field-dialog__title">{{ title }}</h2>

          <form class="field-dialog__form" @submit.prevent="handleSubmit">
            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': error }"
                for="profile-field-input"
              >
                {{ label }}
              </label>

              <input
                v-if="type === 'text' || type === 'date'"
                id="profile-field-input"
                ref="fieldRef"
                v-model="draft"
                class="cy-input"
                :class="{ 'cy-input--invalid': error }"
                :type="type"
                :placeholder="placeholder"
                :maxlength="maxlength || undefined"
              />

              <template v-else-if="type === 'textarea'">
                <textarea
                  id="profile-field-input"
                  ref="fieldRef"
                  v-model="draft"
                  class="cy-input field-dialog__textarea"
                  :class="{ 'cy-input--invalid': error }"
                  rows="3"
                  :placeholder="placeholder"
                  :maxlength="maxlength || undefined"
                />
                <p v-if="maxlength" class="field-dialog__count">
                  {{ draft.length }}/{{ maxlength }}
                </p>
              </template>

              <div v-else class="field-dialog__radios" role="radiogroup" :aria-label="label">
                <button
                  v-for="option in options"
                  :key="option.value"
                  class="field-dialog__radio"
                  :class="{ 'is-active': draft === option.value }"
                  type="button"
                  role="radio"
                  :aria-checked="draft === option.value"
                  @click="draft = option.value"
                >
                  {{ option.label }}
                </button>
              </div>

              <p v-if="error" class="cy-field__error">{{ error }}</p>
              <p v-else-if="hint" class="cy-field__hint">{{ hint }}</p>
            </div>

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

/* 多行输入按内容撑高，序号数在右下角 */
.field-dialog__textarea {
  height: auto;
  padding: 10px 14px;
  line-height: 20px;
  resize: none;
}

.field-dialog__count {
  align-self: flex-end;
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
}

.field-dialog__radios {
  display: flex;
  flex-wrap: wrap;
  gap: var(--cy-space-2);
}

.field-dialog__radio {
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

.field-dialog__radio:hover {
  color: var(--cy-text);
}

.field-dialog__radio.is-active {
  border-color: var(--cy-primary);
  background: var(--cy-primary-soft);
  color: var(--cy-primary-strong);
  font-weight: 600;
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
