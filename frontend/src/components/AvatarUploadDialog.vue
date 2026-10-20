<script setup lang="ts">
// 更换头像弹层：吸附在个人中心的头像正下方，只有「上传图片」按钮与两行尺寸提示。
//
// 不做成带遮罩的居中弹框——内容就在头像下方，用户一眼能对上「改的是这张图」；
// 因此也不设关闭按钮：点弹层以外的页面区域，或按 Escape 即关闭。
// 选图后先在前端拦一遍格式与大小，通过才把文件交给父组件上传；
// 服务端返回的原因由 error 传入内联展示。
//
// 父组件需要把这张头像套进一个 position: relative 的容器里，本组件用绝对定位挂在它下方。

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import AppIcon from '@/components/AppIcon.vue'

/** 允许的类型与大小上限，与后端 avatarExts / avatarMaxSizeMB 保持一致 */
const ACCEPT = 'image/png,image/jpeg,image/webp'
const ACCEPTED_TYPES = ['image/png', 'image/jpeg', 'image/webp']
const MAX_SIZE_MB = 2

/** 建议尺寸，仅作提示；前端不做裁剪 */
const RECOMMENDED_SIZE = 200

const props = withDefaults(
  defineProps<{
    /** 是否展开 */
    open: boolean
    /** 上传中：禁用按钮并屏蔽关闭 */
    uploading?: boolean
    /** 服务端 / 上传失败的原因 */
    error?: string
  }>(),
  { uploading: false, error: '' },
)

const emit = defineEmits<{
  close: []
  submit: [file: File]
}>()

/** 前端拦截的提示（格式 / 大小），与服务端 error 合并展示 */
const localError = ref('')
const errorMessage = computed(() => localError.value || props.error)

const popoverRef = ref<HTMLElement | null>(null)
/** 展开后把焦点落在主操作上 */
const uploadButtonRef = ref<HTMLButtonElement | null>(null)
const fileInputRef = ref<HTMLInputElement | null>(null)

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    requestClose()
  }
}

/** 点弹层以外的页面区域（含头像本身）就关闭 */
function onPointerDown(event: PointerEvent): void {
  const target = event.target as Node | null
  if (target && popoverRef.value?.contains(target)) {
    return
  }
  requestClose()
}

/** 上传中不允许关闭，避免请求回来后状态对不上 */
function requestClose(): void {
  if (props.uploading) {
    return
  }
  emit('close')
}

/** 「上传图片」：清掉上一次的本地提示后再唤起系统文件选择 */
function pickFile(): void {
  localError.value = ''
  fileInputRef.value?.click()
}

/** 选中文件后先在前端拦一遍，通过才交给父组件上传 */
function onFileChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // 清空 value，否则再次选择同一个文件不会触发 change
  input.value = ''
  if (!file) {
    return
  }

  if (!ACCEPTED_TYPES.includes(file.type)) {
    localError.value = '头像仅支持 JPG / PNG / WebP 格式'
    return
  }
  if (file.size > MAX_SIZE_MB * 1024 * 1024) {
    localError.value = `头像不能超过 ${MAX_SIZE_MB} MB`
    return
  }

  localError.value = ''
  emit('submit', file)
}

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      localError.value = ''
      // 捕获阶段监听：先于顶栏的同名监听处理 Escape
      document.addEventListener('keydown', onKeydown, true)
      document.addEventListener('pointerdown', onPointerDown, true)
      await nextTick()
      uploadButtonRef.value?.focus()
      return
    }
    document.removeEventListener('keydown', onKeydown, true)
    document.removeEventListener('pointerdown', onPointerDown, true)
  },
)

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown, true)
  document.removeEventListener('pointerdown', onPointerDown, true)
})
</script>

<template>
  <Transition name="avatar-pop">
    <div v-if="open" ref="popoverRef" class="avatar-pop" role="dialog" aria-label="更换头像">
      <button
        ref="uploadButtonRef"
        class="avatar-pop__upload"
        type="button"
        :disabled="uploading"
        @click="pickFile"
      >
        <AppIcon name="upload" :size="18" />
        <span>{{ uploading ? '上传中…' : '上传图片' }}</span>
      </button>

      <div class="avatar-pop__hints">
        <p>建议尺寸为 {{ RECOMMENDED_SIZE }} x {{ RECOMMENDED_SIZE }} 像素</p>
        <p>每个文件的最大尺寸为 {{ MAX_SIZE_MB }} MB</p>
      </div>

      <p v-if="errorMessage" class="cy-alert cy-alert--error">{{ errorMessage }}</p>

      <input
        ref="fileInputRef"
        class="cy-sr-only"
        type="file"
        :accept="ACCEPT"
        @change="onFileChange"
      />
    </div>
  </Transition>
</template>

<style scoped>
/* 挂在头像正下方：父容器给了 position: relative */
.avatar-pop {
  position: absolute;
  top: calc(100% + var(--cy-space-2));
  left: 50%;
  z-index: 50;
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-4);
  width: 280px;
  padding: var(--cy-space-4);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-lg);
  background: var(--cy-pop-bg);
  box-shadow: var(--cy-shadow-pop);
  text-align: center;
  transform: translateX(-50%);
}

/* 「上传图片」：整行宽的主操作，用浅底承载，不与品牌紫抢注意力 */
.avatar-pop__upload {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--cy-space-2);
  width: 100%;
  padding: var(--cy-space-3);
  border-radius: var(--cy-radius-md);
  background: var(--cy-surface-2);
  color: var(--cy-text);
  font-size: 15px;
  line-height: 22px;
  font-weight: 600;
  transition: background-color var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease);
}

.avatar-pop__upload:hover:not(:disabled) {
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

.avatar-pop__upload:disabled {
  cursor: progress;
}

.avatar-pop__hints {
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
}

/* 淡入 + 轻微收放 */
.avatar-pop-enter-active,
.avatar-pop-leave-active {
  transition: opacity var(--cy-dur-base) var(--cy-ease),
    transform var(--cy-dur-base) var(--cy-ease);
}

.avatar-pop-enter-from,
.avatar-pop-leave-to {
  opacity: 0;
  transform: translateX(-50%) scale(0.96);
}
</style>
