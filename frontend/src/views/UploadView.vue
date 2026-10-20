<script setup lang="ts">
// 管理员投稿页 /admin/upload：大文件走「分片上传 + 断点续传 + 秒传」。
//
// 表单布局取自《窗月-前端UI风格规范》5.4：居中卡片（最大 720px），
// 拖拽上传区（16:9 虚线框）→ 标题 → 简介 → 分类 → 封面 → 提交。
//
// 上传流程：
//   1. 选中文件后先用 Web Worker 分片增量计算整文件 MD5（展示指纹进度，可取消）；
//   2. POST /admin/uploads 初始化：mode=instant 即秒传（直接登记并跳转），
//      mode=multipart 拿到 upload_id / chunk_size / part_total / uploaded；
//   3. 按固定并发（3）直传分片，已上传的分片跳过（断点续传），失败可单独重试；
//      上传中可暂停 / 继续 / 取消；会话信息写入 localStorage 便于下次续传；
//   4. 分片齐全后 POST .../complete 合并登记，再用 PUT /admin/videos/{id} 补封面（失败只提示）。
//
// 前端只挡「扩展名 + 体积」这类一眼能看出的问题，最终裁定仍以后端为准；
// 这是管理员页面：路由 meta.requiresAdmin 保证只有管理员进得来。

import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import { ApiError, toErrorMessage } from '@/api/request'
import {
  abortUpload,
  completeUpload,
  initUpload,
  putPart,
  type UploadedPart,
  type UploadSession,
} from '@/api/upload'
import { updateVideo } from '@/api/video'
import AppIcon from '@/components/AppIcon.vue'
import { VIDEO_CATEGORIES } from '@/types/api'
import type { Md5Response } from '@/workers/md5.worker'

/** 与后端 upload.allowed_ext / max_size_mb 保持一致 */
const VIDEO_ACCEPT = '.mp4,.mov,.mkv,.webm'
const VIDEO_EXTS = ['.mp4', '.mov', '.mkv', '.webm']
const MAX_VIDEO_MB = 2048

/** 与后端 coverExts 保持一致 */
const COVER_ACCEPT = 'image/png,image/jpeg,image/webp'
const COVER_EXTS = ['.jpg', '.jpeg', '.png', '.webp']

/** 与契约里 title / description 的 maxLength 保持一致 */
const MAX_TITLE = 128
const MAX_DESCRIPTION = 512

/** 分片并发数：不宜过高，否则小带宽下每个分片都慢，整体反而更差 */
const CONCURRENCY = 3
/** 断点续传记录的 localStorage key */
const RESUME_KEY = 'cy:admin-upload:pending'
/** 成功提示停留多久后自动跳转视频管理 */
const REDIRECT_DELAY = 1200

/** 页面阶段 */
type Phase = 'idle' | 'hashing' | 'uploading' | 'paused' | 'finalizing' | 'done'
/** 单个分片的状态 */
type PartState = 'pending' | 'uploading' | 'done' | 'failed'

interface PartTask {
  /** 分片序号，从 1 开始 */
  number: number
  state: PartState
  /** 已确认上传的字节数，完成后由服务端返回的 size 填充 */
  size: number
}

/** 本地保存的未完成上传记录，用于下次进入时提示续传 */
interface PendingUpload {
  file_md5: string
  filename: string
  total_size: number
  upload_id: string
}

const router = useRouter()

const title = ref('')
const description = ref('')
/** 0 表示不指定分类 */
const categoryId = ref(0)

const videoFile = ref<File | null>(null)
const coverFile = ref<File | null>(null)
/** 封面的本地预览地址，切换 / 卸载时需要 revoke */
const coverPreview = ref('')

const dragging = ref(false)
const phase = ref<Phase>('idle')
const error = ref('')
/** 封面补充失败时的提示，不算上传失败 */
const coverWarning = ref('')

/** 指纹计算进度 0~100 */
const hashPercent = ref(0)

/** 分片会话 */
const uploadId = ref('')
const chunkSize = ref(0)
const partTotal = ref(0)
const parts = ref<PartTask[]>([])
/** 本次上传的整文件 MD5 */
const fileMd5 = ref('')
/** 本次是否为断点续传（选中的文件与前次未完成记录同指纹） */
const resuming = ref(false)
/** 合并分片失败后允许重试 */
const finalizeFailed = ref(false)

/** 未完成的上传记录 */
const pending = ref<PendingUpload | null>(null)
/** 成功后的提示文案 */
const doneMessage = ref('')

const videoInputRef = ref<HTMLInputElement | null>(null)
const coverInputRef = ref<HTMLInputElement | null>(null)

/** 上传进行中：表单与文件选择一律禁用 */
const busy = computed(
  () =>
    phase.value === 'hashing' ||
    phase.value === 'uploading' ||
    phase.value === 'paused' ||
    phase.value === 'finalizing',
)

const titleCount = computed(() => title.value.length)
const descriptionCount = computed(() => description.value.length)

const doneCount = computed(() => parts.value.filter((part) => part.state === 'done').length)
const failedCount = computed(() => parts.value.filter((part) => part.state === 'failed').length)
/** 整体进度 = 已完成分片数 / 分片总数 */
const percent = computed(() =>
  partTotal.value ? Math.round((doneCount.value / partTotal.value) * 100) : 0,
)
const uploadedBytes = computed(() =>
  parts.value.reduce((sum, part) => (part.state === 'done' ? sum + part.size : sum), 0),
)
const totalBytes = computed(() => videoFile.value?.size ?? 0)

/** 提交按钮文案随阶段变化 */
const submitLabel = computed(() => {
  if (phase.value === 'hashing') {
    return `计算指纹中… ${hashPercent.value}%`
  }
  if (phase.value === 'finalizing') {
    return '正在合并分片…'
  }
  if (phase.value === 'paused') {
    return `已暂停（${percent.value}%）`
  }
  if (phase.value === 'uploading') {
    return `上传中… ${percent.value}%`
  }
  return '发布'
})

// ---- 调度器状态：不需要响应式，用普通变量即可 ----
/** 在途分片数 */
let activeCount = 0
/** 代数：取消 / 重新开始时自增，用于忽略旧的在途分片回调 */
let generation = 0
let hashWorker: Worker | null = null
let redirectTimer: ReturnType<typeof setTimeout> | null = null

/** 小写扩展名：mkv 这类格式浏览器给不出 file.type，只能看后缀 */
function extensionOf(name: string): string {
  const index = name.lastIndexOf('.')
  return index >= 0 ? name.slice(index).toLowerCase() : ''
}

/** 体积文案：超过 1GB 用 GB，读起来更直观 */
function fileSizeText(bytes: number): string {
  if (bytes >= 1024 * 1024 * 1024) {
    return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
  }
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

/** 选中或拖入视频：先挡扩展名与体积 */
function acceptVideo(file: File): void {
  error.value = ''
  if (!VIDEO_EXTS.includes(extensionOf(file.name))) {
    error.value = `视频仅支持 ${VIDEO_EXTS.join(' / ')} 格式`
    return
  }
  if (file.size > MAX_VIDEO_MB * 1024 * 1024) {
    error.value = `视频不能超过 ${MAX_VIDEO_MB} MB`
    return
  }
  videoFile.value = file
}

function onVideoChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // 清空 value，否则再次选择同一个文件不会触发 change
  input.value = ''
  if (file) {
    acceptVideo(file)
  }
}

function pickVideo(): void {
  videoInputRef.value?.click()
}

function removeVideo(): void {
  videoFile.value = null
}

function onDrop(event: DragEvent): void {
  dragging.value = false
  const file = event.dataTransfer?.files?.[0]
  if (file) {
    acceptVideo(file)
  }
}

/** 封面：前端只挡扩展名，体积交给后端裁定 */
function acceptCover(file: File): void {
  error.value = ''
  if (!COVER_EXTS.includes(extensionOf(file.name))) {
    error.value = `封面仅支持 ${COVER_EXTS.join(' / ')} 格式`
    return
  }
  if (coverPreview.value) {
    URL.revokeObjectURL(coverPreview.value)
  }
  coverFile.value = file
  coverPreview.value = URL.createObjectURL(file)
}

function onCoverChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (file) {
    acceptCover(file)
  }
}

function pickCover(): void {
  coverInputRef.value?.click()
}

function removeCover(): void {
  if (coverPreview.value) {
    URL.revokeObjectURL(coverPreview.value)
  }
  coverFile.value = null
  coverPreview.value = ''
}

// ---------- 断点续传记录（localStorage） ----------

function readPendingRecord(): PendingUpload | null {
  try {
    const raw = localStorage.getItem(RESUME_KEY)
    if (!raw) {
      return null
    }
    const value = JSON.parse(raw) as Partial<PendingUpload>
    if (value.file_md5 && value.filename && value.upload_id && typeof value.total_size === 'number') {
      return value as PendingUpload
    }
  } catch {
    // 记录损坏（手工改坏 / 版本变化）时当作没有即可
  }
  return null
}

function savePendingRecord(): void {
  const file = videoFile.value
  if (!file) {
    return
  }
  const record: PendingUpload = {
    file_md5: fileMd5.value,
    filename: file.name,
    total_size: file.size,
    upload_id: uploadId.value,
  }
  pending.value = record
  try {
    localStorage.setItem(RESUME_KEY, JSON.stringify(record))
  } catch {
    // 隐私模式等场景写入失败可忽略，不影响上传本身
  }
}

function clearPendingRecord(): void {
  pending.value = null
  try {
    localStorage.removeItem(RESUME_KEY)
  } catch {
    // 同上
  }
}

/** 用户放弃上一次未完成的上传 */
function discardPending(): void {
  clearPendingRecord()
}

// ---------- 指纹计算（Worker） ----------

function cleanupWorker(): void {
  hashWorker?.terminate()
  hashWorker = null
}

/** 在 Worker 中分片增量计算整文件 MD5，避免主线程卡顿与大内存占用 */
function computeMd5(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const worker = new Worker(new URL('../workers/md5.worker.ts', import.meta.url), {
      type: 'module',
    })
    hashWorker = worker
    worker.onmessage = (event: MessageEvent<Md5Response>) => {
      const message = event.data
      if (message.type === 'progress') {
        hashPercent.value = message.percent
        return
      }
      cleanupWorker()
      if (message.type === 'done') {
        resolve(message.md5)
      } else {
        reject(new Error(message.message))
      }
    }
    worker.onerror = () => {
      cleanupWorker()
      reject(new Error('计算文件指纹失败，请重试'))
    }
    worker.postMessage({ file })
  })
}

/** 取消指纹计算 */
function cancelHash(): void {
  cleanupWorker()
  hashPercent.value = 0
  phase.value = 'idle'
}

// ---------- 分片上传 ----------

function resetUploadState(): void {
  uploadId.value = ''
  chunkSize.value = 0
  partTotal.value = 0
  parts.value = []
  fileMd5.value = ''
  resuming.value = false
  finalizeFailed.value = false
  hashPercent.value = 0
  error.value = ''
  coverWarning.value = ''
}

/** 由服务端的已上传分片初始化分片列表，已上传的直接标记完成 */
function buildParts(total: number, uploaded: UploadedPart[]): PartTask[] {
  const done = new Map<number, number>()
  uploaded.forEach((part) => {
    if (part.part_number) {
      done.set(part.part_number, part.size ?? chunkSize.value)
    }
  })
  const list: PartTask[] = []
  for (let number = 1; number <= total; number += 1) {
    const size = done.get(number)
    list.push(
      size === undefined
        ? { number, state: 'pending', size: 0 }
        : { number, state: 'done', size },
    )
  }
  return list
}

/** 调度器：在途数未达上限且未暂停时，持续取出待传分片并发上传 */
function pump(): void {
  if (phase.value !== 'uploading') {
    return
  }
  while (activeCount < CONCURRENCY) {
    const task = parts.value.find((part) => part.state === 'pending')
    if (!task) {
      break
    }
    void uploadPart(task)
  }
  checkFinished()
}

/** 全部完成且没有在途分片时，进入合并阶段 */
function checkFinished(): void {
  if (phase.value !== 'uploading' || activeCount > 0 || parts.value.length === 0) {
    return
  }
  if (parts.value.every((part) => part.state === 'done')) {
    void finalizeUpload()
  }
}

async function uploadPart(task: PartTask): Promise<void> {
  const gen = generation
  const file = videoFile.value
  activeCount += 1
  task.state = 'uploading'

  try {
    if (!file) {
      throw new Error('文件已丢失，请重新选择')
    }
    const start = (task.number - 1) * chunkSize.value
    const end = Math.min(start + chunkSize.value, file.size)
    const blob = file.slice(start, end)
    const part = await putPart(uploadId.value, task.number, blob)
    if (gen !== generation) {
      return
    }
    task.size = part.size ?? blob.size
    task.state = 'done'
  } catch (e) {
    if (gen !== generation) {
      return
    }
    task.state = 'failed'
    handlePartError(e)
  } finally {
    if (gen === generation) {
      activeCount -= 1
    }
  }
  if (gen === generation) {
    pump()
  }
}

function handlePartError(e: unknown): void {
  if (e instanceof ApiError && e.code === 30010) {
    // 会话失效：无法继续续传，清掉本地记录并让用户重新开始
    generation += 1
    phase.value = 'idle'
    clearPendingRecord()
    resetUploadState()
    error.value = '上传会话已失效，请重新开始上传'
    return
  }
  error.value = '部分分片上传失败，可点击「重试失败分片」继续'
}

/** 重试所有失败分片；暂停中只重置状态，等用户点「继续」 */
function retryFailed(): void {
  if (!parts.value.some((part) => part.state === 'failed')) {
    return
  }
  parts.value.forEach((part) => {
    if (part.state === 'failed') {
      part.state = 'pending'
    }
  })
  error.value = ''
  if (phase.value === 'uploading') {
    pump()
  }
}

function pause(): void {
  if (phase.value !== 'uploading') {
    return
  }
  // 只停止调度新分片，在途的让其自然完成
  phase.value = 'paused'
}

function resume(): void {
  if (phase.value !== 'paused') {
    return
  }
  phase.value = 'uploading'
  pump()
}

async function cancel(): Promise<void> {
  const id = uploadId.value
  generation += 1
  phase.value = 'idle'
  clearPendingRecord()
  resetUploadState()
  activeCount = 0
  if (id) {
    try {
      await abortUpload(id)
    } catch {
      // 中止失败不影响本地复位（会话会由后端超时回收）
    }
  }
}

/** 分片齐全后合并并登记视频，再补封面（补封面失败只提示） */
async function finalizeUpload(): Promise<void> {
  phase.value = 'finalizing'
  finalizeFailed.value = false
  try {
    const video = await completeUpload(uploadId.value)
    if (coverFile.value) {
      try {
        await updateVideo(video.id, {
          title: title.value.trim(),
          description: description.value.trim(),
          category_id: categoryId.value,
          cover: coverFile.value,
        })
      } catch (e) {
        coverWarning.value = `视频已发布，但封面更新失败：${toErrorMessage(e)}`
      }
    }
    clearPendingRecord()
    finish('发布成功')
  } catch (e) {
    if (e instanceof ApiError && e.code === 30010) {
      clearPendingRecord()
      resetUploadState()
      phase.value = 'idle'
      error.value = '上传会话已失效，请重新开始上传'
      return
    }
    finalizeFailed.value = true
    phase.value = 'uploading'
    error.value = `合并分片失败：${toErrorMessage(e)}`
  }
}

/** 合并失败后的手动重试 */
function retryComplete(): void {
  if (phase.value === 'uploading' && !activeCount) {
    void finalizeUpload()
  }
}

function startMultipart(session: UploadSession): void {
  const chunk = session.chunk_size ?? 0
  const total = session.part_total ?? 0
  if (!session.upload_id || chunk <= 0 || total <= 0) {
    error.value = '服务端未返回有效的分片计划，请重试'
    phase.value = 'idle'
    return
  }
  uploadId.value = session.upload_id
  chunkSize.value = chunk
  partTotal.value = total
  parts.value = buildParts(total, session.uploaded ?? [])
  savePendingRecord()
  activeCount = 0
  generation += 1
  phase.value = 'uploading'
  pump()
}

// ---------- 提交流程 ----------

async function submit(): Promise<void> {
  if (busy.value) {
    return
  }
  const file = videoFile.value
  const trimmedTitle = title.value.trim()
  if (!trimmedTitle) {
    error.value = '请填写视频标题'
    return
  }
  if (!file) {
    error.value = '请选择要上传的视频文件'
    return
  }

  error.value = ''
  coverWarning.value = ''
  doneMessage.value = ''
  hashPercent.value = 0
  phase.value = 'hashing'

  let md5 = ''
  try {
    md5 = await computeMd5(file)
  } catch (e) {
    error.value = toErrorMessage(e)
    phase.value = 'idle'
    return
  }
  fileMd5.value = md5
  // 指纹与本地未完成记录一致，说明是在续传
  resuming.value = pending.value !== null && pending.value.file_md5 === md5

  let session: UploadSession
  try {
    session = await initUpload({
      file_md5: md5,
      filename: file.name,
      total_size: file.size,
      content_type: file.type || undefined,
      title: trimmedTitle,
      description: description.value.trim() || undefined,
      category_id: categoryId.value || undefined,
    })
  } catch (e) {
    error.value = toErrorMessage(e)
    phase.value = 'idle'
    return
  }

  if (session.mode === 'instant') {
    clearPendingRecord()
    finish('秒传成功：服务器已有同一文件，已直接登记')
    return
  }
  startMultipart(session)
}

function finish(message: string): void {
  doneMessage.value = message
  phase.value = 'done'
  if (redirectTimer) {
    clearTimeout(redirectTimer)
  }
  redirectTimer = setTimeout(() => {
    redirectTimer = null
    void router.push({ name: 'admin-videos' })
  }, REDIRECT_DELAY)
}

function goAdmin(): void {
  if (redirectTimer) {
    clearTimeout(redirectTimer)
    redirectTimer = null
  }
  void router.push({ name: 'admin-videos' })
}

/** 结果态操作：清空表单再发一条 */
function submitAnother(): void {
  if (redirectTimer) {
    clearTimeout(redirectTimer)
    redirectTimer = null
  }
  doneMessage.value = ''
  title.value = ''
  description.value = ''
  categoryId.value = 0
  removeVideo()
  removeCover()
  resetUploadState()
  phase.value = 'idle'
}

onMounted(() => {
  pending.value = readPendingRecord()
})

onBeforeUnmount(() => {
  cleanupWorker()
  if (redirectTimer) {
    clearTimeout(redirectTimer)
    redirectTimer = null
  }
  if (coverPreview.value) {
    URL.revokeObjectURL(coverPreview.value)
  }
})
</script>

<template>
  <div class="upload cy-container">
    <h1 class="cy-title upload__title">投稿</h1>

    <!-- 结果态：成功后不再展示表单，稍后自动跳转视频管理 -->
    <section v-if="phase === 'done'" class="cy-card upload__card upload__done">
      <span class="upload__done-mark" aria-hidden="true">
        <AppIcon name="play" :size="36" />
      </span>
      <p class="cy-state__title">{{ doneMessage }}</p>
      <p class="cy-state__desc cy-clamp-2">{{ videoFile?.name }}</p>
      <p v-if="coverWarning" class="cy-alert cy-alert--info">{{ coverWarning }}</p>

      <div class="upload__done-actions">
        <button class="cy-btn cy-btn--outline cy-btn--pill" type="button" @click="submitAnother">
          继续投稿
        </button>
        <button class="cy-btn cy-btn--primary cy-btn--pill" type="button" @click="goAdmin">
          去视频管理
        </button>
      </div>
    </section>

    <form v-else class="cy-card upload__card" @submit.prevent="submit">
      <!-- 断点续传入口：检测到未完成记录时提示重新选择同一文件 -->
      <div v-if="pending && !videoFile && phase === 'idle'" class="cy-alert cy-alert--info upload__resume">
        <div class="upload__resume-text">
          <p>检测到未完成的上传，是否继续？</p>
          <p class="cy-caption cy-clamp-1">{{ pending?.filename }}</p>
        </div>
        <div class="upload__resume-actions">
          <button class="cy-btn cy-btn--secondary cy-btn--sm" type="button" @click="pickVideo">
            选择文件继续
          </button>
          <button class="cy-btn cy-btn--ghost cy-btn--sm" type="button" @click="discardPending">
            放弃
          </button>
        </div>
      </div>

      <!-- 拖拽上传区：16:9 虚线框，拖入时整块转成品牌紫 -->
      <div
        v-if="!videoFile"
        class="upload__drop"
        :class="{ 'is-dragging': dragging }"
        @dragenter.prevent="dragging = true"
        @dragover.prevent="dragging = true"
        @dragleave.prevent="dragging = false"
        @drop.prevent="onDrop"
      >
        <input
          ref="videoInputRef"
          class="cy-sr-only"
          type="file"
          :accept="VIDEO_ACCEPT"
          @change="onVideoChange"
        />

        <button class="upload__drop-btn" type="button" :disabled="busy" @click="pickVideo">
          <AppIcon class="upload__drop-icon" name="upload" :size="28" />
          <span class="upload__drop-main">拖拽视频到此处，或点击选择文件</span>
          <span class="upload__drop-hint cy-caption">
            支持 {{ VIDEO_EXTS.join(' / ') }}，单个文件不超过 {{ MAX_VIDEO_MB }} MB
          </span>
        </button>
      </div>

      <div v-else class="upload__file">
        <span class="upload__file-icon" aria-hidden="true">
          <AppIcon name="play" :size="18" />
        </span>
        <div class="upload__file-info">
          <p class="upload__file-name cy-clamp-1">{{ videoFile.name }}</p>
          <p class="cy-caption cy-num">{{ fileSizeText(videoFile.size) }}</p>
        </div>
        <button
          class="cy-btn cy-btn--ghost cy-btn--sm upload__file-remove"
          type="button"
          :disabled="busy"
          @click="removeVideo"
        >
          <AppIcon name="close" :size="16" />
          <span>移除</span>
        </button>
      </div>

      <div class="cy-field">
        <label class="cy-label" for="upload-title">标题</label>
        <input
          id="upload-title"
          v-model="title"
          class="cy-input"
          type="text"
          placeholder="给这段视频起个名字"
          :maxlength="MAX_TITLE"
          :disabled="busy"
        />
        <p class="upload__count cy-caption cy-num">{{ titleCount }}/{{ MAX_TITLE }}</p>
      </div>

      <div class="cy-field">
        <label class="cy-label" for="upload-description">简介（可选）</label>
        <textarea
          id="upload-description"
          v-model="description"
          class="cy-input upload__textarea"
          rows="4"
          placeholder="简单介绍这段内容"
          :maxlength="MAX_DESCRIPTION"
          :disabled="busy"
        />
        <p class="upload__count cy-caption cy-num">{{ descriptionCount }}/{{ MAX_DESCRIPTION }}</p>
      </div>

      <div class="cy-field">
        <label class="cy-label" for="upload-category">分类（可选）</label>
        <div class="upload__select">
          <select
            id="upload-category"
            v-model.number="categoryId"
            class="cy-input upload__select-input"
            :disabled="busy"
          >
            <option :value="0">未分类</option>
            <option v-for="category in VIDEO_CATEGORIES" :key="category.id" :value="category.id">
              {{ category.name }}
            </option>
          </select>
          <AppIcon class="upload__select-arrow" name="chevron-down" :size="18" />
        </div>
      </div>

      <div class="cy-field">
        <span class="cy-label">封面（可选）</span>
        <div class="upload__cover">
          <div class="upload__cover-preview">
            <img v-if="coverPreview" :src="coverPreview" alt="封面预览" />
            <span v-else class="upload__cover-empty" aria-hidden="true">
              <AppIcon name="eye" :size="20" />
            </span>
          </div>

          <div class="upload__cover-actions">
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
              :disabled="busy"
              @click="pickCover"
            >
              {{ coverFile ? '更换封面' : '选择封面' }}
            </button>
            <button
              v-if="coverFile"
              class="cy-btn cy-btn--ghost cy-btn--sm"
              type="button"
              :disabled="busy"
              @click="removeCover"
            >
              移除
            </button>
            <p class="upload__cover-hint cy-caption">
              不填则使用纯色封面；建议 16:9，支持 {{ COVER_EXTS.join(' / ') }}
            </p>
          </div>
        </div>
      </div>

      <!-- 指纹计算：Worker 分片读取，展示进度并可取消 -->
      <div v-if="phase === 'hashing'" class="upload__stage">
        <div
          class="upload__progress"
          role="progressbar"
          aria-label="计算文件指纹"
          :aria-valuenow="hashPercent"
          aria-valuemin="0"
          aria-valuemax="100"
        >
          <div class="upload__progress-bar" :style="{ width: `${hashPercent}%` }" />
        </div>
        <div class="upload__stage-meta">
          <span class="cy-caption cy-num">正在计算文件指纹… {{ hashPercent }}%</span>
          <button class="cy-btn cy-btn--ghost cy-btn--sm" type="button" @click="cancelHash">
            取消
          </button>
        </div>
      </div>

      <!-- 分片上传：进度 + 字节数 + 暂停 / 继续 / 取消 / 重试 -->
      <div
        v-if="phase === 'uploading' || phase === 'paused' || phase === 'finalizing'"
        class="upload__stage"
      >
        <div
          class="upload__progress"
          role="progressbar"
          aria-label="上传进度"
          :aria-valuenow="percent"
          aria-valuemin="0"
          aria-valuemax="100"
        >
          <div class="upload__progress-bar" :style="{ width: `${percent}%` }" />
        </div>

        <div class="upload__stage-meta">
          <span class="cy-caption cy-num">
            {{ percent }}%（{{ doneCount }}/{{ partTotal }} 片）
          </span>
          <span class="cy-caption cy-num">
            {{ fileSizeText(uploadedBytes) }} / {{ fileSizeText(totalBytes) }}
          </span>
        </div>

        <p v-if="resuming" class="cy-caption">已检测到未完成的上传，正在从断点继续…</p>

        <div class="upload__stage-actions">
          <button
            v-if="phase === 'uploading'"
            class="cy-btn cy-btn--outline cy-btn--sm"
            type="button"
            @click="pause"
          >
            暂停
          </button>
          <button
            v-if="phase === 'paused'"
            class="cy-btn cy-btn--primary cy-btn--sm"
            type="button"
            @click="resume"
          >
            继续
          </button>
          <button
            v-if="failedCount > 0 && phase !== 'paused'"
            class="cy-btn cy-btn--secondary cy-btn--sm"
            type="button"
            @click="retryFailed"
          >
            重试失败分片（{{ failedCount }}）
          </button>
          <button
            v-if="finalizeFailed"
            class="cy-btn cy-btn--secondary cy-btn--sm"
            type="button"
            @click="retryComplete"
          >
            重试完成上传
          </button>
          <button
            class="cy-btn cy-btn--ghost cy-btn--sm"
            type="button"
            :disabled="phase === 'finalizing'"
            @click="cancel"
          >
            取消
          </button>
        </div>
      </div>

      <p v-if="error" class="cy-alert cy-alert--error">{{ error }}</p>

      <button type="submit" class="cy-btn cy-btn--primary cy-btn--lg cy-btn--block" :disabled="busy">
        {{ submitLabel }}
      </button>
    </form>
  </div>
</template>

<style scoped>
/* 规范 5.4：表单卡片最大 720px，居中（cy-container 自带 margin auto） */
.upload {
  max-width: 720px;
  padding-bottom: var(--cy-space-7);
}

.upload__title {
  padding: var(--cy-space-5) 0 var(--cy-space-4);
}

.upload__card {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-5);
  padding: var(--cy-space-5);
}

/* ---------- 断点续传入口 ---------- */
.upload__resume {
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--cy-space-3);
}

.upload__resume-text {
  flex: 1;
  min-width: 0;
}

.upload__resume-actions {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
}

/* ---------- 拖拽上传区 ---------- */
.upload__drop {
  aspect-ratio: 16 / 9;
  border: 1px dashed var(--cy-border);
  border-radius: var(--cy-radius-md);
  background: var(--cy-surface-2);
  transition: border-color var(--cy-dur-fast) var(--cy-ease),
    background-color var(--cy-dur-fast) var(--cy-ease);
}

.upload__drop.is-dragging {
  border-color: var(--cy-primary);
  background: var(--cy-primary-soft);
}

/* 整块可点：点击唤起系统文件选择 */
.upload__drop-btn {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--cy-space-2);
  width: 100%;
  height: 100%;
  padding: var(--cy-space-5);
  border-radius: var(--cy-radius-md);
  text-align: center;
}

.upload__drop-icon {
  color: var(--cy-text-muted);
  transition: color var(--cy-dur-fast) var(--cy-ease);
}

.upload__drop-btn:hover .upload__drop-icon {
  color: var(--cy-accent);
}

.upload__drop-main {
  color: var(--cy-text);
  font-size: 15px;
  line-height: 22px;
  font-weight: 500;
}

.upload__drop-hint {
  max-width: 420px;
}

/* ---------- 已选视频 ---------- */
.upload__file {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  padding: var(--cy-space-3) var(--cy-space-4);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-md);
  background: var(--cy-surface-2);
}

.upload__file-icon {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 40px;
  height: 40px;
  border-radius: var(--cy-radius-sm);
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

.upload__file-info {
  flex: 1;
  min-width: 0;
}

.upload__file-name {
  font-size: 14px;
  line-height: 20px;
  font-weight: 500;
}

.upload__file-remove {
  flex-shrink: 0;
}

/* ---------- 表单细节 ---------- */
.upload__count {
  align-self: flex-end;
}

.upload__textarea {
  height: auto;
  padding: 10px 14px;
  line-height: 20px;
  resize: vertical;
}

/* 原生下拉按 .cy-input 统一外观，箭头自己画一个 */
.upload__select {
  position: relative;
}

.upload__select-input {
  appearance: none;
  padding-right: 44px;
}

.upload__select-arrow {
  position: absolute;
  top: 50%;
  right: 14px;
  transform: translateY(-50%);
  color: var(--cy-text-muted);
  pointer-events: none;
}

/* ---------- 封面 ---------- */
.upload__cover {
  display: flex;
  align-items: center;
  gap: var(--cy-space-4);
}

.upload__cover-preview {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 160px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-md);
  overflow: hidden;
  background: var(--cy-surface-2);
}

.upload__cover-preview img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.upload__cover-empty {
  color: var(--cy-text-muted);
}

.upload__cover-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--cy-space-2);
}

.upload__cover-hint {
  flex-basis: 100%;
}

/* ---------- 指纹 / 分片进度 ---------- */
.upload__stage {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-3);
}

.upload__stage-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--cy-space-3);
}

.upload__stage-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--cy-space-2);
}

/* ---------- 进度条 ---------- */
.upload__progress {
  height: 6px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-surface-2);
  overflow: hidden;
}

.upload__progress-bar {
  height: 100%;
  border-radius: var(--cy-radius-full);
  background: var(--cy-primary);
  transition: width var(--cy-dur-base) var(--cy-ease);
}

/* ---------- 结果态 ---------- */
.upload__done {
  align-items: center;
  gap: var(--cy-space-3);
  padding: var(--cy-space-7) var(--cy-space-5);
  text-align: center;
}

.upload__done-mark {
  display: grid;
  place-items: center;
  width: 72px;
  height: 72px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

.upload__done-actions {
  display: flex;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-2);
}

/* 窄屏：封面预览与按钮改为上下排，避免横向挤爆 */
@media (max-width: 599px) {
  .upload__cover {
    flex-direction: column;
    align-items: flex-start;
  }

  .upload__cover-preview {
    width: 100%;
  }
}
</style>