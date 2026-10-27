<script setup lang="ts">
// 观看页：左播放器 + 右「播放队列」两栏。
//
// 右侧队列用现有列表接口按同分类查询（分类为 0 时退化为最新内容），不需要后端新增接口；
// 播完自动播队列里的下一条，队列内容与自动连播开关都持久化在本地。
//
// 互动能力：点赞、收藏、稍后再看按钮 + 观看历史自动上报 + 评论（含评论点赞）。未登录时点击引导登录，
// 不弹出「只在登录后可见」的死提示；历史上报是尽力而为，失败不打断观看。
//
// 续播：进入页面时从互动状态里取回上次观看进度，媒体元数据就绪后跳转到该位置；
// 上报只记真实播放进度，避免把续播点覆盖成 0。
//
// 弹幕：进入时一次性拉取该视频的全部弹幕，用 requestAnimationFrame 以播放进度为时钟
// 把到点的弹幕推到画面上（滚动 / 顶部 / 底部三种模式、多轨道避让），进度暂停时画面随之静止。
// 弹幕开关、颜色选择、设置面板、发送框都在播放器下方；发送者本人与管理员可点击弹幕删除。
// 设置面板支持不透明度 / 字号 / 速度 / 显示区域 / 类型与关键词屏蔽，全部持久化到 localStorage。
//
// 播放体验：支持网页全屏（铺满视口，Esc 退出，不同于浏览器原生全屏）与键盘快捷键
// （空格 / K 播放暂停、←→ 前后 5 秒、↑↓ 调音量、M 静音、F 全屏）；
// 音量与倍速写进 localStorage，下次打开播放页沿用。

import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Hls from 'hls.js'

import { listVideoCollections } from '@/api/collection'
import { createComment, deleteComment, listComments, listReplies, setCommentLike } from '@/api/comment'
import { createDanmaku, deleteDanmaku, listDanmaku } from '@/api/danmaku'
import {
  getInteractionState,
  reportHistory,
  setFavorite,
  setLike,
  setWatchLater,
} from '@/api/interaction'
import { toErrorMessage } from '@/api/request'
import { getVideo, listVideos } from '@/api/video'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ReportDialog from '@/components/ReportDialog.vue'
import StatePanel from '@/components/StatePanel.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { useAuthModalStore } from '@/stores/authModal'
import { usePlayerQueueStore } from '@/stores/playerQueue'
import { useUserStore } from '@/stores/user'
import {
  REPORT_TARGET_VIDEO,
  type Comment,
  type Danmaku,
  type Video,
  type VideoCollectionRef,
} from '@/types/api'
import { formatCount, formatDuration, formatRelativeDate } from '@/utils/format'

/** 简介超过这个长度才显示「展开」，避免短简介下面挂一个没用的按钮 */
const DESCRIPTION_FOLD_LENGTH = 120

/** 评论每页条数，也是「加载更多」的步长 */
const COMMENT_PAGE_SIZE = 10

/** 展开「更多回复」时一次拉取的条数：回复量通常不大，一次取完避免再分页 */
const REPLY_PAGE_SIZE = 50

/** 续播的边界：进度太靠前（<1s）或太接近结尾（距结束 <5s）都从头播 */
const RESUME_MIN_SECONDS = 1
const RESUME_TAIL_SECONDS = 5

/** 倍速档位：控制条倍速菜单与本地偏好校验共用这一份取值 */
const RATE_OPTIONS = [0.5, 0.75, 1, 1.25, 1.5, 2]

/** 键盘快捷键：左右方向键的跳转步长（秒）、上下方向键的音量步长 */
const PLAYER_SEEK_STEP = 5
const PLAYER_VOLUME_STEP = 0.05

/** 播放偏好在 localStorage 里的键名 */
const PLAYER_PREFS_KEY = 'chuangyue:player-prefs'

/** 播放偏好：音量（0~1）与倍速档位，进入播放页时套用，改动后写回本地 */
interface PlayerPrefs {
  volume: number
  rate: number
}

const DEFAULT_PLAYER_PREFS: PlayerPrefs = { volume: 1, rate: 1 }

/** 读取本地播放偏好：字段非法或越界一律回落默认，解析失败整体回落 */
function loadPlayerPrefs(): PlayerPrefs {
  try {
    const raw = localStorage.getItem(PLAYER_PREFS_KEY)
    if (!raw) {
      return { ...DEFAULT_PLAYER_PREFS }
    }
    const parsed = JSON.parse(raw) as Partial<PlayerPrefs>
    const volume = Number(parsed.volume)
    const rate = Number(parsed.rate)
    return {
      volume: Number.isFinite(volume)
        ? Math.min(1, Math.max(0, volume))
        : DEFAULT_PLAYER_PREFS.volume,
      rate: RATE_OPTIONS.includes(rate) ? rate : DEFAULT_PLAYER_PREFS.rate,
    }
  } catch {
    return { ...DEFAULT_PLAYER_PREFS }
  }
}

/** 本地播放偏好：页面打开时读一次，避免每次媒体就绪都碰 localStorage */
const playerPrefs = loadPlayerPrefs()

/**
 * 弹幕「新鲜度」窗口（毫秒）：只有落在当前进度前后这个区间内的弹幕才会上屏。
 * 拖动进度条后游标附近可能攒了一批早已过去的弹幕，靠它把这些陈年弹幕丢掉，
 * 避免画面上突然喷出一屏。
 */
const DANMAKU_SPAWN_WINDOW_MS = 1000

/** 轨道区域的顶部留白（像素），避免第一行弹幕贴住播放器上沿 */
const DANMAKU_LANE_PAD = 8

/** 轨道高度相对字号的倍数：字号越大轨道越高，多行才不会挤在一起 */
const DANMAKU_LANE_HEIGHT_RATIO = 1.5

/** 字号档位：小 / 中 / 大，中为默认 */
const DANMAKU_FONT_OPTIONS = [
  { label: '小', size: 16 },
  { label: '中', size: 20 },
  { label: '大', size: 26 },
] as const

/** 速度档位：控制滚动穿越整屏与顶 / 底弹幕的停留时长（毫秒） */
const DANMAKU_SPEED_OPTIONS = [
  { label: '慢', rollMs: 11000, fixedMs: 5500 },
  { label: '中', rollMs: 8000, fixedMs: 4000 },
  { label: '快', rollMs: 5500, fixedMs: 3000 },
] as const

/** 弹幕可选文字颜色，与后端白名单一一对应 */
const DANMAKU_COLORS = ['#FFFFFF', '#8B5CF6', '#EF4444', '#F59E0B', '#10B981', '#3B82F6'] as const

/** 设置项取值区间 */
const DANMAKU_OPACITY_MIN = 20
const DANMAKU_AREA_MIN = 25

/** 弹幕设置：颜色 / 整体不透明度 / 字号档位 / 速度档位 / 显示区域比例 / 类型屏蔽 */
interface DanmakuSettings {
  color: string
  opacity: number
  fontLevel: number
  speedLevel: number
  areaRatio: number
  blockScroll: boolean
  blockTop: boolean
  blockBottom: boolean
}

/** 默认设置：月白、不透明、中号、中速、显示区域 75%、不屏蔽任何类型 */
const DEFAULT_DANMAKU_SETTINGS: DanmakuSettings = {
  color: DANMAKU_COLORS[0],
  opacity: 100,
  fontLevel: 1,
  speedLevel: 1,
  areaRatio: 75,
  blockScroll: false,
  blockTop: false,
  blockBottom: false,
}

/** 弹幕设置在 localStorage 里的键名 */
const DANMAKU_SETTINGS_KEY = 'chuangyue:danmaku-settings'

/** 把任意值夹到 [min, max] 的整数区间，非法值回落到 fallback */
function clampInt(value: unknown, min: number, max: number, fallback: number): number {
  const n = Math.round(Number(value))
  if (!Number.isFinite(n)) {
    return fallback
  }
  return Math.min(max, Math.max(min, n))
}

/**
 * 读取本地弹幕设置。
 *
 * 逐字段校验而不是整体信任：存档可能来自旧版本，缺字段或越界都回落到默认值；
 * 解析失败（例如手动改坏）时整体回落，保证不影响播放。
 */
function loadDanmakuSettings(): DanmakuSettings {
  try {
    const raw = localStorage.getItem(DANMAKU_SETTINGS_KEY)
    if (!raw) {
      return { ...DEFAULT_DANMAKU_SETTINGS }
    }
    const parsed = JSON.parse(raw) as Partial<DanmakuSettings>
    return {
      color: DANMAKU_COLORS.includes(parsed.color as (typeof DANMAKU_COLORS)[number])
        ? (parsed.color as string)
        : DEFAULT_DANMAKU_SETTINGS.color,
      opacity: clampInt(
        parsed.opacity,
        DANMAKU_OPACITY_MIN,
        100,
        DEFAULT_DANMAKU_SETTINGS.opacity,
      ),
      fontLevel: clampInt(
        parsed.fontLevel,
        0,
        DANMAKU_FONT_OPTIONS.length - 1,
        DEFAULT_DANMAKU_SETTINGS.fontLevel,
      ),
      speedLevel: clampInt(
        parsed.speedLevel,
        0,
        DANMAKU_SPEED_OPTIONS.length - 1,
        DEFAULT_DANMAKU_SETTINGS.speedLevel,
      ),
      areaRatio: clampInt(
        parsed.areaRatio,
        DANMAKU_AREA_MIN,
        100,
        DEFAULT_DANMAKU_SETTINGS.areaRatio,
      ),
      blockScroll: parsed.blockScroll === true,
      blockTop: parsed.blockTop === true,
      blockBottom: parsed.blockBottom === true,
    }
  } catch {
    return { ...DEFAULT_DANMAKU_SETTINGS }
  }
}

/** 弹幕展示模式，与后端 model 保持一致 */
const DANMAKU_MODE_SCROLL = 1
const DANMAKU_MODE_TOP = 2
const DANMAKU_MODE_BOTTOM = 3

/** 屏幕上正在飞的一条弹幕：由渲染引擎维护，不落库 */
interface RenderedDanmaku {
  id: string
  key: string
  content: string
  color: string
  mode: number
  offset: number
  x: number
  itemWidth: number
  startMs: number
  deletable: boolean
}

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()
const authModal = useAuthModalStore()
const queueStore = usePlayerQueueStore()

const video = ref<Video | null>(null)
const loading = ref(true)
const relatedLoading = ref(true)
/** 当前视频所属的合集；接口失败或为空时整块卡片不渲染 */
const videoCollections = ref<VideoCollectionRef[]>([])
const error = ref('')
const playerReady = ref(false)
const descriptionExpanded = ref(false)

const videoEl = ref<HTMLVideoElement | null>(null)

// ===== HLS 播放 =====
//
// 片源经异步转码后是 m3u8 清单。Chrome / Firefox 等不支持原生 HLS 的浏览器
// 交给 hls.js 接管；Safari 原生就能播 m3u8，直接走 <video src>，不重复接管。
// 非 m3u8 的直链（例如转码前的旧数据）也走原生播放。
const hlsSupported = Hls.isSupported()
let hls: Hls | null = null

/** 是否为 HLS 片源：按扩展名判断，避免对直链做无谓处理 */
const isHlsSource = computed(() => (video.value?.play_url ?? '').includes('.m3u8'))

/** 是否由 hls.js 接管播放 */
const useHls = computed(() => isHlsSource.value && hlsSupported)

/** 交给 <video> 的 src：hls.js 接管时留空，由脚本 loadSource 供给 */
const videoSource = computed(() => (useHls.value ? undefined : video.value?.play_url || undefined))

/** 清晰度档位（由转码产出，master 清单里的各档），label 用于菜单展示 */
interface QualityLevel {
  index: number
  label: string
}

const qualityLevels = ref<QualityLevel[]>([])
/** 当前手选档位下标，-1 表示自动（由 hls.js 按带宽自适应） */
const currentLevelIndex = ref(-1)
const qualityMenuOpen = ref(false)
const qualityMenuEl = ref<HTMLElement | null>(null)

/** 控制条清晰度按钮的文案 */
const currentQualityLabel = computed(() => {
  if (currentLevelIndex.value < 0) {
    return '自动'
  }
  return qualityLevels.value.find((item) => item.index === currentLevelIndex.value)?.label ?? '自动'
})
const liked = ref(false)
const favorited = ref(false)
const watchLater = ref(false)
const likeCount = ref(0)
const favoriteCount = ref(0)
const actionPending = ref(false)
const actionError = ref('')

// 上次观看进度（秒），用于媒体元数据就绪后跳转；0 表示从头播
const resumeAt = ref(0)
let resumed = false

// 弹幕状态
const playerEl = ref<HTMLElement | null>(null)
const danmakuEnabled = ref(true)
const danmakuList = ref<Danmaku[]>([])
const danmakuRendered = ref<RenderedDanmaku[]>([])
const danmakuDraft = ref('')
const danmakuSubmitting = ref(false)
const danmakuError = ref('')

/** 弹幕设置面板容器：点面板以外或按 Esc 收起；设置项持久化到 localStorage */
const settingsPanelEl = ref<HTMLElement | null>(null)
const danmakuSettings = ref<DanmakuSettings>(loadDanmakuSettings())
const danmakuSettingsOpen = ref(false)

// 弹幕删除确认弹框
const danmakuDeleteTarget = ref<Danmaku | null>(null)
const danmakuDeleteBusy = ref(false)
const danmakuDeleteError = ref('')

// 弹幕渲染引擎的运行时状态（非响应式，避免每帧触发额外依赖收集）
const playerWidth = ref(0)
const playerHeight = ref(0)
let danmakuPointer = 0 // 下一条待上屏弹幕在 danmakuList 中的下标
let danmakuRafId = 0
let scrollLaneFreeAt: number[] = [] // 各滚动轨道「可再放一条」的视频时刻
let topLaneFreeAt: number[] = []
let bottomLaneFreeAt: number[] = []

// 评论区状态
const comments = ref<Comment[]>([])
const commentTotal = ref(0)
const commentPage = ref(1)
const commentLoading = ref(true)
const commentLoadingMore = ref(false)
const commentError = ref('')
const commentMoreError = ref('')
const commentDraft = ref('')
const commentSubmitting = ref(false)
const commentSubmitError = ref('')

// 评论点赞：记录正在提交的那条评论 id，避免连点造成并发；失败信息单独提示
const commentLikePendingId = ref('')
const commentLikeError = ref('')

// 回复：replyRootId 是当前展开回复框的一级评论 id，
// replyTarget 是被回复的对象（一级评论本身，或它下面某条回复），空表示没有在回复
const replyRootId = ref('')
const replyTarget = ref<Comment | null>(null)
const replyDraft = ref('')
const replySubmitting = ref(false)
const replySubmitError = ref('')

// 「展开更多回复」：记录正在加载的一级评论 id 与错误信息
const replyExpandLoadingId = ref('')
const replyExpandError = ref('')

// 删除确认弹框
const deleteTarget = ref<Comment | null>(null)
const deleteBusy = ref(false)
const deleteError = ref('')

// 举报弹框：视频与评论共用一个弹框，targetType 区分对象
const reportOpen = ref(false)
const reportTargetType = ref<1 | 2>(REPORT_TARGET_VIDEO)
const reportTargetId = ref('')
const reportSuccess = ref('')

const videoId = computed(() => String(route.params.id))

const hasMoreComments = computed(() => comments.value.length < commentTotal.value)

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  playerReady.value = false
  descriptionExpanded.value = false
  video.value = null
  liked.value = false
  favorited.value = false
  watchLater.value = false
  actionError.value = ''
  resumeAt.value = 0
  resumed = false
  videoCollections.value = []

  // 播放器状态跟随视频切换复位：新视频要重新走一次自动播放
  autoplayTried = false
  autoplayMuted.value = false
  playing.value = false
  currentTime.value = 0
  duration.value = 0
  bufferedSec.value = 0
  controlsVisible.value = true
  rateMenuOpen.value = false

  // 评论跟随视频切换重新加载
  commentDraft.value = ''
  commentSubmitError.value = ''
  commentLikeError.value = ''
  closeReply()
  replyExpandLoadingId.value = ''
  replyExpandError.value = ''
  void loadComments(true)

  // 弹幕同样跟随视频切换重置
  danmakuDraft.value = ''
  danmakuError.value = ''
  danmakuDeleteTarget.value = null
  void loadDanmaku()

  try {
    const result = await getVideo(videoId.value)
    video.value = result
    likeCount.value = result.like_count
    favoriteCount.value = result.favorite_count
    // 每页标题带上视频名，浏览器标签页 / 收藏夹里才分得清
    document.title = `${result.title} · 窗月`
    void loadInteraction()
    void loadRelated(result)
    void loadCollections()
  } catch (e) {
    error.value = toErrorMessage(e)
  } finally {
    loading.value = false
  }
  // 播放器渲染出来后才知道它的实际尺寸，轨道数量与弹幕行程都依赖它；
  // 同一时机建立 HLS（片源为 m3u8 时），避免视频元素还没挂载就被接管
  void nextTick(() => {
    syncPlayerSize()
    setupHls()
  })
}

/** 拉取当前用户对该视频的点赞 / 收藏 / 稍后再看状态与上次观看进度；未登录时按未选中处理 */
async function loadInteraction(): Promise<void> {
  if (!userStore.isLoggedIn) {
    liked.value = false
    favorited.value = false
    watchLater.value = false
    resumeAt.value = 0
    return
  }
  try {
    const state = await getInteractionState(videoId.value)
    liked.value = state.liked
    favorited.value = state.favorited
    watchLater.value = state.watch_later
    resumeAt.value = state.progress ?? 0
    tryResume()
  } catch {
    // 状态拉取失败时保持未选中，点击时以后端返回的结果为准
  }
}

/**
 * 媒体元数据就绪后跳到续播点。
 *
 * 状态接口与视频元数据是两个异步来源，谁先到不一定，所以两边都尝试触发，
 * 用 resumed 标记保证只跳一次。
 */
function tryResume(): void {
  const el = videoEl.value
  if (resumed || !el || !Number.isFinite(el.duration) || el.duration <= 0) {
    return
  }
  const target = resumeAt.value
  if (target < RESUME_MIN_SECONDS || target > el.duration - RESUME_TAIL_SECONDS) {
    resumed = true
    return
  }
  el.currentTime = target
  resumed = true
}

/** 视频元数据加载完成：置就绪态、尝试续播并自动播放 */
function onLoadedMetadata(): void {
  playerReady.value = true
  // 换片后先把本地音量 / 倍速套上去，再自动播放，听感沿用上次的选择
  if (!prefsApplied) {
    prefsApplied = true
    applyPlayerPrefs()
  }
  tryResume()
  void tryAutoplay()
}

/** 释放 hls.js 实例：切片、离开页面时都要先销毁，避免媒体源与事件监听泄漏 */
function destroyHls(): void {
  if (hls) {
    hls.destroy()
    hls = null
  }
}

/** 档位文案：优先用分辨率高度，缺失时退化为序号 */
function qualityLabel(height: number | undefined, index: number): string {
  return height ? `${height}p` : `第 ${index + 1} 档`
}

/**
 * 建立 HLS 播放。
 *
 * 只在片源为 m3u8 且浏览器不支持原生 HLS 时创建 hls.js 实例；
 * 其余情况（Safari 原生 HLS、非 m3u8 直链）保持 <video src> 直接播放。
 */
function setupHls(): void {
  destroyHls()
  qualityLevels.value = []
  currentLevelIndex.value = -1
  qualityMenuOpen.value = false

  const el = videoEl.value
  const src = video.value?.play_url ?? ''
  if (!el || !useHls.value || !src) {
    return
  }

  const instance = new Hls()
  hls = instance

  instance.on(Hls.Events.MANIFEST_PARSED, (_event, data) => {
    qualityLevels.value = data.levels.map((level, index) => ({
      index,
      label: qualityLabel(level.height, index),
    }))
    // 清单就绪后即可视为可播放：套用偏好、续播、自动播放，与原生路径保持一致
    playerReady.value = true
    if (!prefsApplied) {
      prefsApplied = true
      applyPlayerPrefs()
    }
    tryResume()
    void tryAutoplay()
  })

  instance.on(Hls.Events.LEVEL_SWITCHED, (_event, data) => {
    // 自动模式下档位会随带宽频繁变化，只记录用户手动选择的档位
    if (currentLevelIndex.value >= 0) {
      currentLevelIndex.value = data.level
    }
  })

  instance.on(Hls.Events.ERROR, (_event, data) => {
    if (!data.fatal) {
      return
    }
    if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
      instance.startLoad()
      return
    }
    if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
      instance.recoverMediaError()
      return
    }
    // 无法恢复的错误：释放实例并放行加载态，由 <video> 的 error 事件兜底
    playerReady.value = true
    destroyHls()
  })

  instance.loadSource(src)
  instance.attachMedia(el)
}

/** 切换清晰度：-1 为自动，其余为具体档位下标 */
function chooseQuality(index: number): void {
  if (hls) {
    hls.currentLevel = index
  }
  currentLevelIndex.value = index
  qualityMenuOpen.value = false
}

// ===== 自建播放控制条 =====
//
// 原生 controls 已隐藏，播放 / 进度 / 时间 / 音量 / 倍速 / 全屏全部自绘，
// 这样才能做出竖直音量条。以下状态都以 video 元素的真实状态为准，只做镜像。

const playing = ref(false)
const currentTime = ref(0)
const duration = ref(0)
const bufferedSec = ref(0)
const volume = ref(1)
const muted = ref(false)
const playbackRate = ref(1)
const isFullscreen = ref(false)

/** 网页全屏（剧场）：播放器铺满视口，但不进入浏览器全屏；按 Esc 退出 */
const isWebFullscreen = ref(false)

/** 播放偏好是否已套用到当前媒体：换片后要重新套用一次 */
let prefsApplied = false

/** 播放偏好写回本地的防抖计时器（拖音量条会连续触发） */
let prefsSaveTimer = 0

/** 画中画：原生 controls 已移除，改由自建按钮调用 Picture-in-Picture API */
const pipSupported = ref(false)
const isPiP = ref(false)

/** 自动播放被浏览器拦下、已静音兜底时给出的显式入口 */
const autoplayMuted = ref(false)
let autoplayTried = false

/** 控制条显隐：播放中鼠标静止一段时间后淡出，暂停时常驻 */
const controlsVisible = ref(true)
let controlsHideTimer = 0

/** 倍速菜单 */
const rateMenuOpen = ref(false)

const progressEl = ref<HTMLElement | null>(null)
const rateMenuEl = ref<HTMLElement | null>(null)

const playedPercent = computed(() =>
  duration.value > 0 ? Math.min(100, (currentTime.value / duration.value) * 100) : 0,
)

const bufferedPercent = computed(() =>
  duration.value > 0 ? Math.min(100, (bufferedSec.value / duration.value) * 100) : 0,
)

/** 音量滑条的填充比例：静音时按 0 展示 */
const volumePercent = computed(() => Math.round((muted.value ? 0 : volume.value) * 100))

/** 竖直音量条：轨道填充比例透传给样式，由 input 自身的 runnable-track 绘制（与圆点同源，天然对齐） */
const volumeSliderStyle = computed<Record<string, string>>(() => ({
  '--volume-pct': `${volumePercent.value}%`,
}))

/** 秒转 mm:ss / hh:mm:ss */
function formatTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) {
    return '00:00'
  }
  const total = Math.floor(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const pad = (n: number): string => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

function togglePlay(): void {
  const el = videoEl.value
  if (!el) {
    return
  }
  if (el.paused) {
    void el.play()
  } else {
    el.pause()
  }
}

function onPlay(): void {
  playing.value = true
  scheduleControlsHide()
}

function onPause(): void {
  playing.value = false
  controlsVisible.value = true
  window.clearTimeout(controlsHideTimer)
  // 暂停时也记一次进度，避免长时间观看中途关页丢进度
  void reportWatch(videoEl.value?.currentTime ?? 0)
}

/**
 * 播放结束：记满观看进度，并按队列自动播下一条。
 * 关闭自动连播、或已是队列最后一条时，停在结束画面上。
 */
function onEnded(): void {
  void reportWatch(duration.value)
  const next = queueStore.autoPlay ? queueStore.nextAfter(videoId.value) : null
  if (next) {
    void router.push({ name: 'video-detail', params: { id: next.id } })
  }
}

function onTimeUpdate(): void {
  const el = videoEl.value
  if (!el) {
    return
  }
  currentTime.value = el.currentTime
  duration.value = Number.isFinite(el.duration) ? el.duration : 0
}

function onProgress(): void {
  const el = videoEl.value
  if (!el || el.buffered.length === 0) {
    return
  }
  bufferedSec.value = el.buffered.end(el.buffered.length - 1)
}

function onVolumeChange(): void {
  const el = videoEl.value
  if (!el) {
    return
  }
  volume.value = el.volume
  muted.value = el.muted
}

function onRateChange(): void {
  const el = videoEl.value
  if (el) {
    playbackRate.value = el.playbackRate
  }
}

function toggleMute(): void {
  const el = videoEl.value
  if (!el) {
    return
  }
  el.muted = !el.muted
}

function onVolumeInput(event: Event): void {
  const el = videoEl.value
  if (!el) {
    return
  }
  const value = Number((event.target as HTMLInputElement).value)
  el.volume = value
  el.muted = value === 0
}

/** 倍速：设置到媒体元素并同步本地状态 */
function chooseRate(value: number): void {
  const el = videoEl.value
  if (el) {
    el.playbackRate = value
  }
  // 直接同步本地状态，避免个别浏览器 ratechange 事件时序导致按钮文案不刷新
  playbackRate.value = value
  rateMenuOpen.value = false
}

/** 套用本地播放偏好（音量 / 倍速）：媒体元数据就绪后调用一次 */
function applyPlayerPrefs(): void {
  const el = videoEl.value
  if (!el) {
    return
  }
  el.volume = playerPrefs.volume
  el.playbackRate = playerPrefs.rate
}

/** 偏好写回本地：做防抖，拖音量条会连发 volumechange，不必每次都落盘 */
function schedulePrefsSave(): void {
  window.clearTimeout(prefsSaveTimer)
  prefsSaveTimer = window.setTimeout(() => {
    try {
      localStorage.setItem(
        PLAYER_PREFS_KEY,
        JSON.stringify({ volume: volume.value, rate: playbackRate.value }),
      )
    } catch {
      // 隐私模式等场景下不可写，忽略即可，偏好仅本次会话生效
    }
  }, 400)
}

/** 快捷键左右方向键：按秒数前后跳，夹在 [0, duration] 内 */
function seekBy(seconds: number): void {
  const el = videoEl.value
  if (!el || !Number.isFinite(el.duration)) {
    return
  }
  el.currentTime = Math.min(el.duration, Math.max(0, el.currentTime + seconds))
}

/** 快捷键上下方向键：音量增减，加回来时自动解除静音 */
function nudgeVolume(delta: number): void {
  const el = videoEl.value
  if (!el) {
    return
  }
  const next = Math.min(1, Math.max(0, Number((el.volume + delta).toFixed(2))))
  el.volume = next
  el.muted = next === 0
}

/** 网页全屏（剧场）开 / 关：只切状态，位置与滚动锁定交给 watch 处理 */
function toggleWebFullscreen(): void {
  isWebFullscreen.value = !isWebFullscreen.value
}

/** 焦点在输入类元素里时不抢快捷键（评论框 / 弹幕框里按空格不该暂停视频） */
function isTypingTarget(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null
  if (!el) {
    return false
  }
  return (
    el.tagName === 'INPUT' ||
    el.tagName === 'TEXTAREA' ||
    el.tagName === 'SELECT' ||
    el.isContentEditable
  )
}

async function toggleFullscreen(): Promise<void> {
  const el = playerEl.value
  if (!el) {
    return
  }
  try {
    if (document.fullscreenElement) {
      await document.exitFullscreen()
    } else {
      // 原生全屏与网页全屏不叠加：进原生全屏前先退出网页全屏
      isWebFullscreen.value = false
      await el.requestFullscreen()
    }
  } catch {
    // 浏览器不支持或被用户拒绝时忽略
  }
}

function onFullscreenChange(): void {
  isFullscreen.value = document.fullscreenElement === playerEl.value
  if (!document.fullscreenElement) {
    // 退出原生全屏后焦点会落回控制按钮，且按键盘交互渲染紫色焦点环；
    // 等浏览器完成焦点恢复后再移除，避免按钮上残留圆环
    requestAnimationFrame(clearControlFocus)
  }
}

/** 退出全屏后移除控制按钮上的焦点，避免残留紫色焦点环（鼠标操作后尤其明显） */
function clearControlFocus(): void {
  const active = document.activeElement as HTMLElement | null
  if (active && active !== document.body && playerEl.value?.contains(active)) {
    active.blur()
  }
}

/** 画中画：进入 / 退出（浏览器未开启画中画时按钮不展示） */
async function togglePiP(): Promise<void> {
  const el = videoEl.value
  if (!el) {
    return
  }
  try {
    if (document.pictureInPictureElement) {
      await document.exitPictureInPicture()
    } else {
      await el.requestPictureInPicture()
    }
  } catch {
    // 浏览器不支持或被用户拒绝时忽略
  }
}

function onEnterPiP(): void {
  isPiP.value = true
}

function onLeavePiP(): void {
  isPiP.value = false
}

/** 播放中且没有面板打开时，鼠标静止 3 秒后淡出控制条 */
function scheduleControlsHide(): void {
  window.clearTimeout(controlsHideTimer)
  if (!playing.value || rateMenuOpen.value) {
    return
  }
  controlsHideTimer = window.setTimeout(() => {
    controlsVisible.value = false
  }, 3000)
}

function showControls(): void {
  controlsVisible.value = true
  scheduleControlsHide()
}

function onPlayerLeave(): void {
  if (playing.value && !rateMenuOpen.value) {
    controlsVisible.value = false
  }
}

// 进度条拖拽：按下时把指针捕获在进度条上，移出播放器也能继续拖
let scrubbing = false

function applyScrub(clientX: number): void {
  const el = videoEl.value
  const bar = progressEl.value
  if (!el || !bar || duration.value <= 0) {
    return
  }
  const rect = bar.getBoundingClientRect()
  const ratio = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width))
  currentTime.value = ratio * duration.value
  el.currentTime = currentTime.value
}

function onScrubStart(event: PointerEvent): void {
  if (duration.value <= 0) {
    return
  }
  scrubbing = true
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
  applyScrub(event.clientX)
}

function onScrubMove(event: PointerEvent): void {
  if (scrubbing) {
    applyScrub(event.clientX)
  }
}

function onScrubEnd(event: PointerEvent): void {
  if (!scrubbing) {
    return
  }
  scrubbing = false
  ;(event.currentTarget as HTMLElement).releasePointerCapture(event.pointerId)
}

/**
 * 打开视频后自动播放。
 *
 * 浏览器的「有声自动播放」策略可能直接拒绝 play()，此时静音重试（静音自动播放总是允许），
 * 并给出「点此开启声音」的入口，避免用户以为视频没声音。
 */
async function tryAutoplay(): Promise<void> {
  const el = videoEl.value
  if (!el || autoplayTried) {
    return
  }
  autoplayTried = true
  try {
    await el.play()
  } catch {
    el.muted = true
    try {
      await el.play()
      autoplayMuted.value = true
    } catch {
      // 仍被拦下就交给用户手动点播放
    }
  }
}

/** 用户点了静音提示：取消静音并继续播放 */
function unmuteAndPlay(): void {
  const el = videoEl.value
  autoplayMuted.value = false
  if (!el) {
    return
  }
  el.muted = false
  if (el.paused) {
    void el.play()
  }
}

/** 点赞 / 取消点赞；未登录先引导登录 */
async function toggleLike(): Promise<void> {
  if (!userStore.isLoggedIn) {
    authModal.openModal('login')
    return
  }
  if (actionPending.value) {
    return
  }
  actionPending.value = true
  actionError.value = ''
  try {
    const result = await setLike(videoId.value, !liked.value)
    liked.value = result.liked
    likeCount.value = result.like_count
  } catch (e) {
    actionError.value = toErrorMessage(e)
  } finally {
    actionPending.value = false
  }
}

/** 收藏 / 取消收藏；未登录先引导登录 */
async function toggleFavorite(): Promise<void> {
  if (!userStore.isLoggedIn) {
    authModal.openModal('login')
    return
  }
  if (actionPending.value) {
    return
  }
  actionPending.value = true
  actionError.value = ''
  try {
    const result = await setFavorite(videoId.value, !favorited.value)
    favorited.value = result.favorited
    favoriteCount.value = result.favorite_count
  } catch (e) {
    actionError.value = toErrorMessage(e)
  } finally {
    actionPending.value = false
  }
}

/** 稍后再看 / 移出稍后再看；未登录先引导登录 */
async function toggleWatchLater(): Promise<void> {
  if (!userStore.isLoggedIn) {
    authModal.openModal('login')
    return
  }
  if (actionPending.value) {
    return
  }
  actionPending.value = true
  actionError.value = ''
  try {
    const result = await setWatchLater(videoId.value, !watchLater.value)
    watchLater.value = result.watch_later
  } catch (e) {
    actionError.value = toErrorMessage(e)
  } finally {
    actionPending.value = false
  }
}

/** 上报观看进度：尽力而为，失败静默 */
async function reportWatch(progress: number): Promise<void> {
  if (!userStore.isLoggedIn || !video.value) {
    return
  }
  try {
    await reportHistory(videoId.value, progress)
  } catch {
    // 观看历史不是关键路径，失败不打断观看
  }
}

/**
 * 拉取当前视频所属的合集。
 *
 * 合集信息是观看页的附加信息：接口失败时静默隐藏整块卡片，
 * 不弹错误提示，也不影响播放器与队列的正常使用。
 */
async function loadCollections(): Promise<void> {
  try {
    videoCollections.value = await listVideoCollections(videoId.value)
  } catch {
    videoCollections.value = []
  }
}

async function loadRelated(target: Video): Promise<void> {
  // 顺着队列看下来时队列已就位，不再重建，避免打乱用户自己的顺序
  if (queueStore.has(target.id)) {
    relatedLoading.value = false
    return
  }
  relatedLoading.value = true
  try {
    const result = await listVideos({
      page: 1,
      page_size: 12,
      category_id: target.category_id || undefined,
    })
    const list = (result.list ?? []).filter((item) => item.id !== target.id).slice(0, 8)
    queueStore.setQueue([target, ...list])
  } catch {
    // 取推荐失败不影响观看，队列至少保留当前视频
    queueStore.setQueue([target])
  } finally {
    relatedLoading.value = false
  }
}

/** 当前字号（像素），同时用于粗略估算文本宽度以做轨道避让 */
const danmakuFontSize = computed(() => DANMAKU_FONT_OPTIONS[danmakuSettings.value.fontLevel].size)

/** 单条弹幕所在轨道的高度（像素）：随字号缩放，行距才不会挤在一起 */
const danmakuLaneHeight = computed(() => Math.round(danmakuFontSize.value * DANMAKU_LANE_HEIGHT_RATIO))

/** 滚动弹幕穿越整屏的耗时（毫秒），由速度档位决定 */
const danmakuRollMs = computed(() => DANMAKU_SPEED_OPTIONS[danmakuSettings.value.speedLevel].rollMs)

/** 顶部 / 底部弹幕的停留时长（毫秒），由速度档位决定 */
const danmakuFixedMs = computed(
  () => DANMAKU_SPEED_OPTIONS[danmakuSettings.value.speedLevel].fixedMs,
)

/** 弹幕层行内样式：整体不透明度与字号由设置驱动 */
const danmakuLayerStyle = computed(() => ({
  opacity: String(danmakuSettings.value.opacity / 100),
  fontSize: `${danmakuFontSize.value}px`,
}))

/** 播放器能容纳的弹幕轨道数：随播放器高度与「显示区域」设置自适应 */
const danmakuLaneCount = computed(() => {
  const area = (playerHeight.value * danmakuSettings.value.areaRatio) / 100 - DANMAKU_LANE_PAD
  return Math.max(3, Math.min(12, Math.floor(area / danmakuLaneHeight.value)))
})

// ===== 弹幕渲染引擎 =====

/** 拉取当前视频的全部弹幕，并重置渲染引擎状态 */
async function loadDanmaku(): Promise<void> {
  danmakuList.value = []
  danmakuRendered.value = []
  danmakuPointer = 0
  scrollLaneFreeAt = []
  topLaneFreeAt = []
  bottomLaneFreeAt = []
  try {
    danmakuList.value = await listDanmaku(videoId.value)
  } catch {
    // 弹幕拿不到不影响观看，静默降级为「暂无弹幕」
    danmakuList.value = []
  }
}

/** 同步播放器尺寸：轨道数量与弹幕行程都依赖它 */
function syncPlayerSize(): void {
  const el = playerEl.value
  if (!el) {
    return
  }
  playerWidth.value = el.clientWidth
  playerHeight.value = el.clientHeight
}

/** 粗略估算弹幕文本宽度：中日韩文字按一个字号宽计，够用于轨道避让 */
function estimateDanmakuWidth(content: string): number {
  return content.length * danmakuFontSize.value + 16
}

/** 二分查找第一条时间点不早于 timeMs 的弹幕下标 */
function lowerBound(list: Danmaku[], timeMs: number): number {
  let lo = 0
  let hi = list.length
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (list[mid].time_ms < timeMs) {
      lo = mid + 1
    } else {
      hi = mid
    }
  }
  return lo
}

/** 挑一条最空闲的轨道：优先给最早腾出来的那条 */
function pickLane(freeAt: number[]): number {
  const count = danmakuLaneCount.value
  while (freeAt.length < count) {
    freeAt.push(0)
  }
  let best = 0
  for (let i = 1; i < count; i += 1) {
    if (freeAt[i] < freeAt[best]) {
      best = i
    }
  }
  return best
}

/** 当前用户是否能删除该弹幕：发送者本人或管理员 */
function canDeleteDanmaku(d: Danmaku): boolean {
  if (userStore.isAdmin) {
    return true
  }
  const me = userStore.profile
  return !!me && String(me.id) === d.user_id
}

/** 该弹幕是否被设置屏蔽：类型屏蔽（滚动 / 顶部 / 底部） */
function isDanmakuBlocked(d: Danmaku): boolean {
  const s = danmakuSettings.value
  if (d.mode === DANMAKU_MODE_SCROLL && s.blockScroll) {
    return true
  }
  if (d.mode === DANMAKU_MODE_TOP && s.blockTop) {
    return true
  }
  if (d.mode === DANMAKU_MODE_BOTTOM && s.blockBottom) {
    return true
  }
  return false
}

/** 把一条弹幕交给渲染引擎上屏：nowMs 为当前进度，startMs 为该弹幕的入屏时刻 */
function pushDanmaku(d: Danmaku, nowMs: number, startMs: number): void {
  const width = playerWidth.value
  if (width <= 0 || isDanmakuBlocked(d)) {
    return
  }
  const itemWidth = estimateDanmakuWidth(d.content)

  let duration: number = danmakuFixedMs.value
  let lane = 0
  if (d.mode === DANMAKU_MODE_SCROLL) {
    duration = danmakuRollMs.value
    lane = pickLane(scrollLaneFreeAt)
    // 这条弹幕的尾巴完全进入画面后，本轨道才能再放一条
    scrollLaneFreeAt[lane] = startMs + (danmakuRollMs.value * itemWidth) / (width + itemWidth)
  } else if (d.mode === DANMAKU_MODE_TOP) {
    lane = pickLane(topLaneFreeAt)
    topLaneFreeAt[lane] = startMs + danmakuFixedMs.value
  } else if (d.mode === DANMAKU_MODE_BOTTOM) {
    lane = pickLane(bottomLaneFreeAt)
    bottomLaneFreeAt[lane] = startMs + danmakuFixedMs.value
  }

  const progress = Math.min(1, Math.max(0, (nowMs - startMs) / duration))
  danmakuRendered.value.push({
    id: d.id,
    key: `${d.id}-${startMs}`,
    content: d.content,
    color: d.color,
    mode: d.mode,
    offset: DANMAKU_LANE_PAD + lane * danmakuLaneHeight.value,
    x: width - progress * (width + itemWidth),
    itemWidth,
    startMs,
    deletable: canDeleteDanmaku(d),
  })
}

/** 把进度已到的弹幕逐条上屏 */
function spawnDanmaku(nowMs: number): void {
  const list = danmakuList.value
  while (danmakuPointer < list.length && list[danmakuPointer].time_ms <= nowMs) {
    const d = list[danmakuPointer]
    danmakuPointer += 1
    // 拖进度条后可能攒了一批「已经过去很久」的弹幕，直接丢弃
    if (nowMs - d.time_ms > DANMAKU_SPAWN_WINDOW_MS) {
      continue
    }
    pushDanmaku(d, nowMs, d.time_ms)
  }
}

/** 按当前进度推进每条在屏弹幕，并移除已飞完的 */
function advanceDanmaku(nowMs: number): void {
  const width = playerWidth.value
  const next: RenderedDanmaku[] = []
  for (const item of danmakuRendered.value) {
    const duration = item.mode === DANMAKU_MODE_SCROLL ? danmakuRollMs.value : danmakuFixedMs.value
    const progress = (nowMs - item.startMs) / duration
    if (progress >= 1) {
      continue
    }
    if (item.mode === DANMAKU_MODE_SCROLL && width > 0) {
      item.x = width - progress * (width + item.itemWidth)
    }
    next.push(item)
  }
  danmakuRendered.value = next
}

/** 跳转后清屏并重新定位游标，避免旧弹幕残留在错误位置 */
function resetDanmakuAt(nowMs: number): void {
  danmakuRendered.value = []
  scrollLaneFreeAt = []
  topLaneFreeAt = []
  bottomLaneFreeAt = []
  danmakuPointer = lowerBound(danmakuList.value, nowMs - DANMAKU_SPAWN_WINDOW_MS)
}

/** 弹幕主循环：以视频进度为时钟，暂停时进度不走，画面自然静止 */
function danmakuTick(): void {
  danmakuRafId = requestAnimationFrame(danmakuTick)
  const el = videoEl.value
  if (!el || !danmakuEnabled.value || el.paused) {
    return
  }
  const nowMs = Math.round(el.currentTime * 1000)
  spawnDanmaku(nowMs)
  advanceDanmaku(nowMs)
}

/** 进度跳转后重排弹幕 */
function onDanmakuSeek(): void {
  const el = videoEl.value
  if (!el) {
    return
  }
  syncPlayerSize()
  resetDanmakuAt(Math.round(el.currentTime * 1000))
}

/** 单条弹幕的行内样式：滚动用 translateX 推进，顶 / 底用固定偏移 */
function danmakuStyle(item: RenderedDanmaku): Record<string, string> {
  if (item.mode === DANMAKU_MODE_SCROLL) {
    return { color: item.color, top: `${item.offset}px`, transform: `translateX(${item.x}px)` }
  }
  if (item.mode === DANMAKU_MODE_TOP) {
    return { color: item.color, top: `${item.offset}px` }
  }
  if (item.mode === DANMAKU_MODE_BOTTOM) {
    return { color: item.color, bottom: `${item.offset}px` }
  }
  return { color: item.color, top: `${item.offset}px` }
}

/** 把新弹幕按时间轴顺序插入本地列表，并同步游标避免重复上屏 */
function insertDanmaku(d: Danmaku): void {
  const list = danmakuList.value
  let i = list.length
  while (i > 0 && list[i - 1].time_ms > d.time_ms) {
    i -= 1
  }
  list.splice(i, 0, d)
  if (i <= danmakuPointer) {
    danmakuPointer += 1
  }
}

/** 选择弹幕颜色：写进设置，随设置一起持久化 */
function chooseDanmakuColor(color: string): void {
  danmakuSettings.value.color = color
}

/** 点在设置面板 / 倍速菜单以外就收起；触发按钮在各自容器内，交给它们自己的 click 切换 */
function onDocumentClick(event: MouseEvent): void {
  const target = event.target as Node | null
  if (danmakuSettingsOpen.value && !settingsPanelEl.value?.contains(target)) {
    danmakuSettingsOpen.value = false
  }
  if (rateMenuOpen.value && !rateMenuEl.value?.contains(target)) {
    rateMenuOpen.value = false
  }
  if (qualityMenuOpen.value && !qualityMenuEl.value?.contains(target)) {
    qualityMenuOpen.value = false
  }
}

/**
 * 全局键盘：Esc 收起浮层 / 退出网页全屏，其余是播放快捷键。
 *
 * 焦点在输入框里、或按了 Ctrl/Cmd/Alt 时不抢按键，避免打断评论与弹幕输入。
 */
function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    if (isWebFullscreen.value) {
      isWebFullscreen.value = false
      // 退出网页全屏后焦点仍在按钮上，移除以免残留紫色焦点环
      clearControlFocus()
      return
    }
    danmakuSettingsOpen.value = false
    rateMenuOpen.value = false
    qualityMenuOpen.value = false
    return
  }
  if (event.ctrlKey || event.metaKey || event.altKey || isTypingTarget(event.target)) {
    return
  }
  if (!videoEl.value || !video.value?.play_url) {
    return
  }
  switch (event.key) {
    case ' ':
    case 'k':
    case 'K':
      event.preventDefault()
      togglePlay()
      break
    case 'ArrowLeft':
      event.preventDefault()
      seekBy(-PLAYER_SEEK_STEP)
      break
    case 'ArrowRight':
      event.preventDefault()
      seekBy(PLAYER_SEEK_STEP)
      break
    case 'ArrowUp':
      event.preventDefault()
      nudgeVolume(PLAYER_VOLUME_STEP)
      break
    case 'ArrowDown':
      event.preventDefault()
      nudgeVolume(-PLAYER_VOLUME_STEP)
      break
    case 'm':
    case 'M':
      event.preventDefault()
      toggleMute()
      break
    case 'f':
    case 'F':
      event.preventDefault()
      void toggleFullscreen()
      break
    default:
      return
  }
  // 用了快捷键说明人正在看播放器，把控制条亮出来
  showControls()
}

/** 恢复默认弹幕设置 */
function resetDanmakuSettings(): void {
  danmakuSettings.value = { ...DEFAULT_DANMAKU_SETTINGS }
}

/**
 * 设置变化后重排当前画面。
 *
 * 字号 / 速度 / 显示区域 / 屏蔽规则都会改变轨道与行程，旧弹幕若留在原处会错位，
 * 因此按当前进度重新铺一遍；不透明度只走 CSS，无需重排（也避免拖动时闪烁）。
 */
function reformatDanmaku(): void {
  if (!danmakuEnabled.value) {
    return
  }
  syncPlayerSize()
  resetDanmakuAt(Math.round((videoEl.value?.currentTime ?? 0) * 1000))
}

/** 发送弹幕：未登录先引导登录，弹幕关闭时不允许发送 */
async function submitDanmaku(): Promise<void> {
  if (!danmakuEnabled.value) {
    return
  }
  if (!userStore.isLoggedIn) {
    authModal.openModal('login')
    return
  }
  const content = danmakuDraft.value.trim()
  if (!content || danmakuSubmitting.value) {
    return
  }
  danmakuSubmitting.value = true
  danmakuError.value = ''
  try {
    const nowMs = Math.max(0, Math.round((videoEl.value?.currentTime ?? 0) * 1000))
    const created = await createDanmaku(videoId.value, {
      content,
      time_ms: nowMs,
      color: danmakuSettings.value.color,
      mode: DANMAKU_MODE_SCROLL,
    })
    insertDanmaku(created)
    danmakuDraft.value = ''
    // 立即上屏给发送者反馈；暂停时让它先滑进来一点，否则停在画面外看不见
    const startMs = videoEl.value?.paused ? Math.max(0, nowMs - 800) : nowMs
    pushDanmaku(created, nowMs, startMs)
  } catch (e) {
    danmakuError.value = toErrorMessage(e)
  } finally {
    danmakuSubmitting.value = false
  }
}

/** 点击弹幕：对可删的弹幕弹出确认框 */
function onDanmakuClick(item: RenderedDanmaku): void {
  if (!item.deletable) {
    return
  }
  const target = danmakuList.value.find((d) => d.id === item.id)
  if (!target) {
    return
  }
  danmakuDeleteError.value = ''
  danmakuDeleteTarget.value = target
}

/** 关闭弹幕删除确认框 */
function closeDeleteDanmaku(): void {
  if (danmakuDeleteBusy.value) {
    return
  }
  danmakuDeleteTarget.value = null
  danmakuDeleteError.value = ''
}

/** 确认删除弹幕：软删成功后同时从本地列表与画面移除 */
async function confirmDeleteDanmaku(): Promise<void> {
  const target = danmakuDeleteTarget.value
  if (!target) {
    return
  }
  danmakuDeleteBusy.value = true
  danmakuDeleteError.value = ''
  try {
    await deleteDanmaku(target.id)
    const list = danmakuList.value
    const idx = list.findIndex((d) => d.id === target.id)
    if (idx >= 0) {
      list.splice(idx, 1)
      if (idx < danmakuPointer) {
        danmakuPointer -= 1
      }
    }
    danmakuRendered.value = danmakuRendered.value.filter((d) => d.id !== target.id)
    danmakuDeleteTarget.value = null
  } catch (e) {
    danmakuDeleteError.value = toErrorMessage(e)
  } finally {
    danmakuDeleteBusy.value = false
  }
}

function initialOf(title: string): string {
  return title.trim().charAt(0) || '窗'
}

/** 评论作者展示名：优先昵称，回退用户名 */
function commentAuthor(c: Comment): string {
  return c.nickname || c.username
}

/** 当前用户是否能删除该评论：作者本人或管理员 */
function canDeleteComment(c: Comment): boolean {
  if (userStore.isAdmin) {
    return true
  }
  const me = userStore.profile
  return !!me && String(me.id) === c.user_id
}

/** 加载评论列表；reset 为 true 时回到第一页 */
async function loadComments(reset: boolean): Promise<void> {
  if (reset) {
    commentLoading.value = true
    commentError.value = ''
  } else {
    commentLoadingMore.value = true
    commentMoreError.value = ''
  }

  const targetPage = reset ? 1 : commentPage.value + 1
  try {
    const result = await listComments(videoId.value, {
      page: targetPage,
      page_size: COMMENT_PAGE_SIZE,
    })
    const list = result.list ?? []
    const merged = reset ? list : [...comments.value, ...list]
    comments.value = [...new Map(merged.map((item) => [item.id, item])).values()]
    commentTotal.value = result.total ?? 0
    commentPage.value = result.page ?? targetPage
  } catch (e) {
    if (reset) {
      commentError.value = toErrorMessage(e)
      comments.value = []
      commentTotal.value = 0
    } else {
      commentMoreError.value = toErrorMessage(e)
    }
  } finally {
    commentLoading.value = false
    commentLoadingMore.value = false
  }
}

/** 某条评论（或回复）当前是否正被回复：用于输入框展开态与按钮高亮 */
function isReplying(c: Comment): boolean {
  return replyTarget.value?.id === c.id
}

/** 某个一级评论下是否该展示内联回复框 */
function showReplyBox(root: Comment): boolean {
  return replyRootId.value === root.id
}

/** 输入框占位文案：回复某条回复时带上 @昵称，回复一级评论时不带 */
function replyPlaceholder(): string {
  const target = replyTarget.value
  if (target && target.parent_id !== '0') {
    return `回复 @${commentAuthor(target)}`
  }
  return '回复评论…'
}

/** 收起回复框并清空草稿 */
function closeReply(): void {
  replyRootId.value = ''
  replyTarget.value = null
  replyDraft.value = ''
  replySubmitError.value = ''
}

/** 点「回复」：未登录先引导登录；再次点击同一条则收起 */
function toggleReply(root: Comment, target: Comment): void {
  if (!userStore.isLoggedIn) {
    authModal.openModal('login')
    return
  }
  if (replyRootId.value === root.id && replyTarget.value?.id === target.id) {
    closeReply()
    return
  }
  replyRootId.value = root.id
  replyTarget.value = target
  replyDraft.value = ''
  replySubmitError.value = ''
}

/** 提交回复：后端统一挂到该一级评论下，成功后插入本地列表并同步回复数 */
async function submitReply(root: Comment): Promise<void> {
  const target = replyTarget.value
  const content = replyDraft.value.trim()
  if (!target || !content || replySubmitting.value) {
    return
  }
  replySubmitting.value = true
  replySubmitError.value = ''
  try {
    const created = await createComment(videoId.value, content, target.id)
    root.replies = [...(root.replies ?? []), created]
    root.reply_count += 1
    closeReply()
  } catch (e) {
    replySubmitError.value = toErrorMessage(e)
  } finally {
    replySubmitting.value = false
  }
}

/** 展开某个一级评论下的全部回复 */
async function expandReplies(root: Comment): Promise<void> {
  if (replyExpandLoadingId.value) {
    return
  }
  replyExpandLoadingId.value = root.id
  replyExpandError.value = ''
  try {
    const result = await listReplies(root.id, { page: 1, page_size: REPLY_PAGE_SIZE })
    root.replies = result.list ?? []
    root.reply_count = result.total ?? root.reply_count
  } catch (e) {
    replyExpandError.value = toErrorMessage(e)
  } finally {
    replyExpandLoadingId.value = ''
  }
}

/** 发表评论；未登录先引导登录 */
async function submitComment(): Promise<void> {
  if (!userStore.isLoggedIn) {
    authModal.openModal('login')
    return
  }
  const content = commentDraft.value.trim()
  if (!content || commentSubmitting.value) {
    return
  }
  commentSubmitting.value = true
  commentSubmitError.value = ''
  try {
    const created = await createComment(videoId.value, content)
    comments.value = [created, ...comments.value]
    commentTotal.value += 1
    commentDraft.value = ''
  } catch (e) {
    commentSubmitError.value = toErrorMessage(e)
  } finally {
    commentSubmitting.value = false
  }
}

/** 点赞 / 取消点赞评论；未登录先引导登录，成功后用接口返回值刷新该条评论 */
async function toggleCommentLike(c: Comment): Promise<void> {
  if (!userStore.isLoggedIn) {
    authModal.openModal('login')
    return
  }
  if (commentLikePendingId.value) {
    return
  }
  commentLikePendingId.value = c.id
  commentLikeError.value = ''
  try {
    const result = await setCommentLike(c.id, !c.liked)
    c.liked = result.liked
    c.like_count = result.like_count
  } catch (e) {
    commentLikeError.value = toErrorMessage(e)
  } finally {
    commentLikePendingId.value = ''
  }
}

/** 打开删除确认弹框 */
function askDeleteComment(c: Comment): void {
  deleteError.value = ''
  deleteTarget.value = c
}

/** 关闭删除确认弹框 */
function closeDeleteComment(): void {
  if (deleteBusy.value) {
    return
  }
  deleteTarget.value = null
  deleteError.value = ''
}

/** 确认删除评论：软删成功后从本地列表移除（删除回复时同步所属一级评论的回复数） */
async function confirmDeleteComment(): Promise<void> {
  const target = deleteTarget.value
  if (!target) {
    return
  }
  deleteBusy.value = true
  deleteError.value = ''
  try {
    await deleteComment(target.id)
    if (target.parent_id !== '0') {
      const root = comments.value.find((item) => item.id === target.parent_id)
      if (root) {
        root.replies = (root.replies ?? []).filter((reply) => reply.id !== target.id)
        root.reply_count = Math.max(0, root.reply_count - 1)
      }
      if (replyTarget.value?.id === target.id) {
        closeReply()
      }
    } else {
      comments.value = comments.value.filter((item) => item.id !== target.id)
      commentTotal.value = Math.max(0, commentTotal.value - 1)
    }
    deleteTarget.value = null
  } catch (e) {
    deleteError.value = toErrorMessage(e)
  } finally {
    deleteBusy.value = false
  }
}

/**
 * 打开举报弹框：视频与评论共用一个弹框，靠 targetType / targetId 区分。
 * 未登录先引导登录，不弹「只在登录后可见」的死提示。
 */
function openReport(targetType: 1 | 2, targetId: string): void {
  if (!userStore.isLoggedIn) {
    authModal.openModal('login')
    return
  }
  reportTargetType.value = targetType
  reportTargetId.value = targetId
  reportSuccess.value = ''
  reportOpen.value = true
}

function closeReport(): void {
  reportOpen.value = false
}

/** 举报成功：关闭弹框并给一行轻量反馈，处理进度由管理端跟进 */
function onReportSubmitted(): void {
  reportOpen.value = false
  reportSuccess.value = '举报已提交，我们会尽快核实处理。'
}

// 登录态变化时重新拉取互动状态（登录弹框关闭后按钮态即时同步）
watch(
  () => userStore.isLoggedIn,
  (loggedIn) => {
    if (loggedIn) {
      void loadInteraction()
    } else {
      liked.value = false
      favorited.value = false
      watchLater.value = false
      resumeAt.value = 0
    }
  },
)

// 重新打开弹幕时从当前进度重新铺，避免残留旧位置
watch(danmakuEnabled, (on) => {
  if (!on) {
    danmakuRendered.value = []
    danmakuSettingsOpen.value = false
    return
  }
  const el = videoEl.value
  syncPlayerSize()
  resetDanmakuAt(Math.round((el?.currentTime ?? 0) * 1000))
})

// 设置变化后持久化；写入失败（隐私模式等）不影响本次会话内的生效
watch(
  danmakuSettings,
  (value) => {
    try {
      localStorage.setItem(DANMAKU_SETTINGS_KEY, JSON.stringify(value))
    } catch {
      // 存不进去就算了，设置仅在本次会话生效
    }
  },
  { deep: true },
)

// 字号 / 速度 / 显示区域 / 屏蔽规则变化后重排画面（不透明度不在此列，见 reformatDanmaku）
watch(
  () => [
    danmakuSettings.value.fontLevel,
    danmakuSettings.value.speedLevel,
    danmakuSettings.value.areaRatio,
    danmakuSettings.value.blockScroll,
    danmakuSettings.value.blockTop,
    danmakuSettings.value.blockBottom,
  ],
  () => reformatDanmaku(),
  { deep: true },
)

watch(videoId, () => void load())

/**
 * 网页全屏：铺满视口期间锁住页面滚动，退出时恢复；尺寸变化后重算弹幕轨道。
 * 播放器位置由 CSS 的 fixed 定位负责，这里只管滚动与尺寸这两件 CSS 管不到的事。
 */
watch(isWebFullscreen, (on) => {
  document.documentElement.style.overflow = on ? 'hidden' : ''
  void nextTick(syncPlayerSize)
})

/** 音量 / 倍速改动后合并写回本地，下次进入播放页沿用 */
watch([volume, playbackRate], () => schedulePrefsSave())

onMounted(() => {
  void load()
  // 浏览器是否开启画中画能力：不支持时不展示画中画按钮
  pipSupported.value = document.pictureInPictureEnabled === true
  window.addEventListener('resize', syncPlayerSize)
  document.addEventListener('click', onDocumentClick)
  document.addEventListener('keydown', onKeydown)
  document.addEventListener('fullscreenchange', onFullscreenChange)
  danmakuRafId = requestAnimationFrame(danmakuTick)
})

// 离开页面时补报真实进度，续播才有落点；暂停时也记一次，避免长时间观看中途关页丢进度
onBeforeUnmount(() => {
  void reportWatch(videoEl.value?.currentTime ?? 0)
  destroyHls()
  // 偏好是防抖写入的，离开页面时补一次，避免最后 400ms 内的改动丢掉
  window.clearTimeout(prefsSaveTimer)
  schedulePrefsSave()
  document.documentElement.style.overflow = ''
  window.removeEventListener('resize', syncPlayerSize)
  document.removeEventListener('click', onDocumentClick)
  document.removeEventListener('keydown', onKeydown)
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  window.clearTimeout(controlsHideTimer)
  cancelAnimationFrame(danmakuRafId)
})
</script>

<template>
  <div class="detail cy-container">
    <div v-if="loading" class="detail__layout">
      <div class="detail__main">
        <div class="detail__skeleton-player cy-skeleton" />
        <div class="detail__skeleton-title cy-skeleton" />
        <div class="detail__skeleton-line cy-skeleton" />
      </div>
      <div class="detail__side">
        <div class="detail__skeleton-side-title cy-skeleton" />
        <div v-for="index in 4" :key="index" class="detail__skeleton-side cy-skeleton" />
      </div>
    </div>

    <StatePanel
      v-else-if="error"
      variant="error"
      title="视频加载失败"
      :description="error"
      action-label="返回首页"
      @action="router.push({ name: 'home' })"
    />

    <div v-else-if="video" class="detail__layout">
      <div class="detail__main">
        <div
          ref="playerEl"
          class="player"
          :class="{ 'is-idle': !controlsVisible, 'is-web-fullscreen': isWebFullscreen }"
          @mousemove="showControls"
          @mouseleave="onPlayerLeave"
        >
          <video
            v-if="video.play_url"
            ref="videoEl"
            class="player__video"
            :src="videoSource"
            :poster="video.cover_url || undefined"
            playsinline
            @click="togglePlay"
            @loadedmetadata="onLoadedMetadata"
            @loadeddata="onLoadedMetadata"
            @timeupdate="onTimeUpdate"
            @durationchange="onTimeUpdate"
            @progress="onProgress"
            @play="onPlay"
            @pause="onPause"
            @ended="onEnded"
            @seeked="onDanmakuSeek"
            @volumechange="onVolumeChange"
            @ratechange="onRateChange"
            @enterpictureinpicture="onEnterPiP"
            @leavepictureinpicture="onLeavePiP"
            @error="playerReady = true"
          />
          <p v-else class="player__empty">该视频暂无可播放地址</p>

          <!-- 弹幕层：叠加在画面上，空层不拦截鼠标，只有弹幕本身可点；不透明度 / 字号由设置驱动 -->
          <div
            v-show="danmakuEnabled && !!video.play_url"
            class="danmaku"
            :style="danmakuLayerStyle"
          >
            <span
              v-for="item in danmakuRendered"
              :key="item.key"
              class="danmaku__item"
              :class="[`danmaku__item--${item.mode}`, { 'is-deletable': item.deletable }]"
              :style="danmakuStyle(item)"
              @click="onDanmakuClick(item)"
            >
              {{ item.content }}
            </span>
          </div>

          <!-- 月相加载：缓冲期间用月的盈缺表达进度 -->
          <div v-if="video.play_url && !playerReady" class="player__loading">
            <AppIcon class="cy-moon-pulse" name="moon" :size="40" />
          </div>

          <!-- 自建控制条：原生 controls 已隐藏，播放 / 进度 / 时间 / 音量 / 倍速 / 全屏全部自绘 -->
          <div v-if="video.play_url" class="pc" :class="{ 'is-hidden': !controlsVisible }">
            <div
              ref="progressEl"
              class="pc__progress"
              @pointerdown="onScrubStart"
              @pointermove="onScrubMove"
              @pointerup="onScrubEnd"
              @pointercancel="onScrubEnd"
            >
              <div class="pc__progress-track">
                <div class="pc__progress-buffered" :style="{ width: `${bufferedPercent}%` }" />
                <div class="pc__progress-played" :style="{ width: `${playedPercent}%` }">
                  <span class="pc__progress-knob" />
                </div>
              </div>
            </div>

            <div class="pc__bar">
              <button
                class="pc__btn"
                type="button"
                :aria-label="playing ? '暂停' : '播放'"
                @click="togglePlay"
              >
                <AppIcon :name="playing ? 'pause' : 'play'" :size="22" />
              </button>

              <span class="pc__time cy-num">
                {{ formatTime(currentTime) }} / {{ formatTime(duration) }}
              </span>

              <span class="pc__spacer" />

              <!-- 音量：点按钮静音 / 取消静音，悬停向上展开竖直滑条 -->
              <div class="pc__volume">
                <button
                  class="pc__btn"
                  type="button"
                  :aria-label="muted || volume === 0 ? '取消静音' : '静音'"
                  @click="toggleMute"
                >
                  <AppIcon :name="muted || volume === 0 ? 'volume-mute' : 'volume'" :size="20" />
                </button>
                <div class="pc__volume-panel">
                  <input
                    class="pc__volume-slider"
                    type="range"
                    min="0"
                    max="1"
                    step="0.05"
                    :value="muted ? 0 : volume"
                    :style="volumeSliderStyle"
                    aria-label="音量"
                    @input="onVolumeInput"
                  />
                </div>
              </div>

              <!-- 清晰度：仅当 hls.js 接管且解析出多档时展示，档位来自转码产出的 master 清单 -->
              <div v-if="qualityLevels.length > 0" ref="qualityMenuEl" class="pc__rate">
                <button
                  class="pc__btn pc__btn--text"
                  type="button"
                  aria-label="清晰度"
                  aria-haspopup="true"
                  :aria-expanded="qualityMenuOpen"
                  @click="qualityMenuOpen = !qualityMenuOpen"
                >
                  <AppIcon name="quality" :size="20" />
                  <span class="pc__quality-label">{{ currentQualityLabel }}</span>
                </button>
                <div v-if="qualityMenuOpen" class="pc__rate-menu">
                  <button
                    class="pc__rate-item"
                    :class="{ 'is-active': currentLevelIndex < 0 }"
                    type="button"
                    @click="chooseQuality(-1)"
                  >
                    自动
                  </button>
                  <button
                    v-for="level in qualityLevels"
                    :key="level.index"
                    class="pc__rate-item"
                    :class="{ 'is-active': currentLevelIndex === level.index }"
                    type="button"
                    @click="chooseQuality(level.index)"
                  >
                    {{ level.label }}
                  </button>
                </div>
              </div>

              <div ref="rateMenuEl" class="pc__rate">
                <button
                  class="pc__btn"
                  type="button"
                  aria-label="倍速"
                  aria-haspopup="true"
                  :aria-expanded="rateMenuOpen"
                  @click="rateMenuOpen = !rateMenuOpen"
                >
                  <AppIcon name="speed" :size="20" />
                </button>
                <div v-if="rateMenuOpen" class="pc__rate-menu">
                  <button
                    v-for="option in RATE_OPTIONS"
                    :key="option"
                    class="pc__rate-item"
                    :class="{ 'is-active': playbackRate === option }"
                    type="button"
                    @click="chooseRate(option)"
                  >
                    {{ option === 1 ? '正常' : `${option}x` }}
                  </button>
                </div>
              </div>

              <button
                v-if="pipSupported"
                class="pc__btn"
                type="button"
                :aria-label="isPiP ? '退出画中画' : '画中画'"
                @click="togglePiP"
              >
                <AppIcon name="pip" :size="20" />
              </button>

              <button
                class="pc__btn"
                type="button"
                :aria-label="isWebFullscreen ? '退出网页全屏' : '网页全屏'"
                @click="toggleWebFullscreen"
              >
                <AppIcon name="web-fullscreen" :size="20" />
              </button>

              <button
                class="pc__btn"
                type="button"
                :aria-label="isFullscreen ? '退出全屏' : '全屏'"
                @click="toggleFullscreen"
              >
                <AppIcon :name="isFullscreen ? 'fullscreen-exit' : 'fullscreen'" :size="20" />
              </button>
            </div>
          </div>

          <!-- 自动播放被拦下、已静音兜底时给一个显式入口 -->
          <button v-if="autoplayMuted" class="pc__unmute" type="button" @click="unmuteAndPlay">
            <AppIcon name="volume-mute" :size="16" />
            <span>已静音自动播放，点此开启声音</span>
          </button>
        </div>

        <!-- 弹幕工具条：开关 + 设置 + 发送 -->
        <div class="danmaku-bar">
          <button
            class="danmaku-bar__toggle"
            :class="{ 'is-off': !danmakuEnabled }"
            type="button"
            @click="danmakuEnabled = !danmakuEnabled"
          >
            <AppIcon :name="danmakuEnabled ? 'eye' : 'eye-off'" :size="18" />
            <span>{{ danmakuEnabled ? '弹幕开' : '弹幕关' }}</span>
          </button>

          <!-- 弹幕设置：颜色 / 不透明度 / 字号 / 速度 / 显示区域 / 类型屏蔽，点击向上展开 -->
          <div ref="settingsPanelEl" class="danmaku-bar__settings">
            <button
              class="danmaku-bar__settings-trigger"
              :class="{ 'is-open': danmakuSettingsOpen }"
              type="button"
              aria-haspopup="true"
              :aria-expanded="danmakuSettingsOpen"
              :disabled="!danmakuEnabled"
              aria-label="弹幕设置"
              title="弹幕设置"
              @click="danmakuSettingsOpen = !danmakuSettingsOpen"
            >
              <AppIcon name="settings" :size="18" />
            </button>

            <div v-if="danmakuSettingsOpen" class="danmaku-settings">
              <div class="danmaku-settings__row">
                <span class="danmaku-settings__label">颜色</span>
                <div class="danmaku-settings__options">
                  <button
                    v-for="color in DANMAKU_COLORS"
                    :key="color"
                    class="danmaku-settings__swatch"
                    :class="{ 'is-active': danmakuSettings.color === color }"
                    :style="{ background: color }"
                    type="button"
                    :aria-label="`选择弹幕颜色 ${color}`"
                    @click="chooseDanmakuColor(color)"
                  />
                </div>
              </div>

              <div class="danmaku-settings__row">
                <span class="danmaku-settings__label">不透明度</span>
                <input
                  v-model.number="danmakuSettings.opacity"
                  class="danmaku-settings__range"
                  type="range"
                  :min="DANMAKU_OPACITY_MIN"
                  max="100"
                  step="10"
                  aria-label="弹幕不透明度"
                />
                <span class="danmaku-settings__value cy-num">{{ danmakuSettings.opacity }}%</span>
              </div>

              <div class="danmaku-settings__row">
                <span class="danmaku-settings__label">字号</span>
                <div class="danmaku-settings__options">
                  <button
                    v-for="(opt, index) in DANMAKU_FONT_OPTIONS"
                    :key="opt.label"
                    class="danmaku-settings__chip"
                    :class="{ 'is-active': danmakuSettings.fontLevel === index }"
                    type="button"
                    @click="danmakuSettings.fontLevel = index"
                  >
                    {{ opt.label }}
                  </button>
                </div>
              </div>

              <div class="danmaku-settings__row">
                <span class="danmaku-settings__label">速度</span>
                <div class="danmaku-settings__options">
                  <button
                    v-for="(opt, index) in DANMAKU_SPEED_OPTIONS"
                    :key="opt.label"
                    class="danmaku-settings__chip"
                    :class="{ 'is-active': danmakuSettings.speedLevel === index }"
                    type="button"
                    @click="danmakuSettings.speedLevel = index"
                  >
                    {{ opt.label }}
                  </button>
                </div>
              </div>

              <div class="danmaku-settings__row">
                <span class="danmaku-settings__label">显示区域</span>
                <input
                  v-model.number="danmakuSettings.areaRatio"
                  class="danmaku-settings__range"
                  type="range"
                  :min="DANMAKU_AREA_MIN"
                  max="100"
                  step="5"
                  aria-label="弹幕显示区域"
                />
                <span class="danmaku-settings__value cy-num">{{ danmakuSettings.areaRatio }}%</span>
              </div>

              <div class="danmaku-settings__row">
                <span class="danmaku-settings__label">屏蔽类型</span>
                <div class="danmaku-settings__options">
                  <button
                    class="danmaku-settings__chip"
                    :class="{ 'is-active': danmakuSettings.blockScroll }"
                    type="button"
                    @click="danmakuSettings.blockScroll = !danmakuSettings.blockScroll"
                  >
                    滚动
                  </button>
                  <button
                    class="danmaku-settings__chip"
                    :class="{ 'is-active': danmakuSettings.blockTop }"
                    type="button"
                    @click="danmakuSettings.blockTop = !danmakuSettings.blockTop"
                  >
                    顶部
                  </button>
                  <button
                    class="danmaku-settings__chip"
                    :class="{ 'is-active': danmakuSettings.blockBottom }"
                    type="button"
                    @click="danmakuSettings.blockBottom = !danmakuSettings.blockBottom"
                  >
                    底部
                  </button>
                </div>
              </div>

              <button class="danmaku-settings__reset" type="button" @click="resetDanmakuSettings">
                恢复默认
              </button>
            </div>
          </div>

          <input
            v-model="danmakuDraft"
            class="danmaku-bar__input"
            type="text"
            maxlength="100"
            :placeholder="danmakuEnabled ? '发个弹幕见证当下' : '弹幕已关闭，开启后可发送'"
            :disabled="danmakuSubmitting || !danmakuEnabled"
            @keyup.enter="submitDanmaku"
          />

          <button
            class="cy-btn cy-btn--primary cy-btn--pill danmaku-bar__send"
            type="button"
            :disabled="danmakuSubmitting || !danmakuEnabled || !danmakuDraft.trim()"
            @click="submitDanmaku"
          >
            {{ danmakuSubmitting ? '发送中…' : '发送' }}
          </button>
        </div>
        <p v-if="danmakuError" class="cy-alert cy-alert--error danmaku-bar__error">
          {{ danmakuError }}
        </p>

        <!-- 点赞 / 收藏：圆形图标按钮 + 计数，紧跟在弹幕发送条下方 -->
        <div class="detail__actions">
          <div class="detail__action">
            <button
              class="cy-btn cy-btn--icon detail__action-btn"
              :class="liked ? 'is-active' : 'cy-btn--secondary'"
              type="button"
              :disabled="actionPending"
              :aria-pressed="liked"
              :aria-label="liked ? '取消点赞' : '点赞'"
              @click="toggleLike"
            >
              <AppIcon name="thumbs-up" :size="24" />
            </button>
            <span class="detail__action-count cy-num">{{ formatCount(likeCount) }}</span>
          </div>

          <div class="detail__action">
            <button
              class="cy-btn cy-btn--icon detail__action-btn"
              :class="favorited ? 'is-active' : 'cy-btn--secondary'"
              type="button"
              :disabled="actionPending"
              :aria-pressed="favorited"
              :aria-label="favorited ? '取消收藏' : '收藏'"
              @click="toggleFavorite"
            >
              <AppIcon name="star" :size="24" />
            </button>
            <span class="detail__action-count cy-num">{{ formatCount(favoriteCount) }}</span>
          </div>

          <!-- 稍后再看：独立的私人待看清单，无对外计数，因此只放按钮 -->
          <div class="detail__action">
            <button
              class="cy-btn cy-btn--icon detail__action-btn"
              :class="watchLater ? 'is-active' : 'cy-btn--secondary'"
              type="button"
              :disabled="actionPending"
              :aria-pressed="watchLater"
              :aria-label="watchLater ? '移出稍后再看' : '稍后再看'"
              @click="toggleWatchLater"
            >
              <AppIcon name="later" :size="24" />
            </button>
          </div>

          <!-- 举报：低频操作，用图标按钮靠操作组右端对齐，不与点赞 / 收藏抢视觉 -->
          <button
            class="detail__report"
            type="button"
            aria-label="举报视频"
            @click="openReport(REPORT_TARGET_VIDEO, videoId)"
          >
            <AppIcon name="report" :size="20" />
          </button>
        </div>

        <p v-if="actionError" class="cy-alert cy-alert--error detail__action-error">
          {{ actionError }}
        </p>

        <p v-if="reportSuccess" class="cy-alert cy-alert--success detail__report-success">
          {{ reportSuccess }}
        </p>

        <h1 class="detail__title cy-display">{{ video.title }}</h1>

        <div class="detail__meta">
          <span>窗月官方</span>
          <span><span class="cy-num">{{ formatCount(video.view_count) }}</span> 次播放</span>
          <span>{{ formatRelativeDate(video.created_at) }}</span>
        </div>

        <!-- 简介：不设标题，正文与视频标题左对齐，下方用一条浅线与评论区分隔 -->
        <section class="detail__section detail__section--desc">
          <p class="detail__desc cy-body" :class="{ 'is-collapsed': !descriptionExpanded }">
            {{ video.description || '暂无简介' }}
          </p>
          <button
            v-if="video.description.length > DESCRIPTION_FOLD_LENGTH"
            class="detail__toggle"
            type="button"
            @click="descriptionExpanded = !descriptionExpanded"
          >
            {{ descriptionExpanded ? '收起' : '展开更多' }}
          </button>
        </section>

        <section class="detail__section">
          <h2 class="detail__section-title cy-title">
            评论
            <span v-if="!commentLoading" class="detail__comment-count cy-num">{{ commentTotal }}</span>
          </h2>

          <!-- 发表评论：未登录时输入框仍可见，提交时引导登录；点进输入框才露出发表按钮（见 .composer:focus-within） -->
          <div class="composer">
            <textarea
              v-model="commentDraft"
              class="composer__input"
              rows="2"
              maxlength="1000"
              placeholder="说点什么…"
              :disabled="commentSubmitting"
            />
            <p v-if="commentSubmitError" class="cy-alert cy-alert--error composer__error">
              {{ commentSubmitError }}
            </p>
            <div class="composer__foot">
              <button
                class="cy-btn cy-btn--primary cy-btn--pill composer__submit"
                type="button"
                :disabled="commentSubmitting || !commentDraft.trim()"
                @click="submitComment"
              >
                {{ commentSubmitting ? '发表中…' : '发表评论' }}
              </button>
            </div>
          </div>

          <p v-if="commentLikeError" class="cy-alert cy-alert--error comment__like-error">
            {{ commentLikeError }}
          </p>

          <div v-if="commentLoading" class="comment-list">
            <div v-for="index in 3" :key="index" class="comment__skeleton cy-skeleton" />
          </div>

          <StatePanel
            v-else-if="commentError"
            variant="error"
            title="评论加载失败"
            :description="commentError"
            action-label="重新加载"
            @action="loadComments(true)"
          />

          <p v-else-if="comments.length === 0" class="cy-caption comment__empty">
            还没有评论，来说两句吧。
          </p>

          <div v-else class="comment-list">
            <article v-for="item in comments" :key="item.id" class="comment">
              <UserAvatar :src="item.avatar" :size="36" :alt="commentAuthor(item)" />
              <div class="comment__body">
                <div class="comment__head">
                  <span class="comment__author">{{ commentAuthor(item) }}</span>
                </div>
                <p class="comment__text">{{ item.content }}</p>
                <!-- 发布时间、点赞、回复与删除按钮同处一行，与评论正文左对齐 -->
                <div class="comment__foot">
                  <span class="comment__time">{{ formatRelativeDate(item.created_at) }}</span>
                  <button
                    class="comment__like"
                    :class="{ 'is-liked': item.liked }"
                    type="button"
                    :disabled="commentLikePendingId === item.id"
                    :aria-pressed="item.liked"
                    :aria-label="item.liked ? '取消点赞' : '点赞'"
                    @click="toggleCommentLike(item)"
                  >
                    <AppIcon name="thumbs-up" :size="16" />
                    <span v-if="item.like_count > 0" class="cy-num">
                      {{ formatCount(item.like_count) }}
                    </span>
                  </button>
                  <button
                    class="comment__reply-btn"
                    :class="{ 'is-active': isReplying(item) }"
                    type="button"
                    aria-label="回复"
                    @click="toggleReply(item, item)"
                  >
                    回复
                  </button>
                  <button
                    v-if="canDeleteComment(item)"
                    class="comment__delete"
                    type="button"
                    aria-label="删除评论"
                    @click="askDeleteComment(item)"
                  >
                    <AppIcon name="trash" :size="16" />
                  </button>
                </div>

                <!-- 回复列表：默认展示最早的若干条，其余用「展开更多回复」拉取 -->
                <div v-if="item.replies.length > 0 || item.reply_count > 0" class="reply-list">
                  <div v-for="reply in item.replies" :key="reply.id" class="reply">
                    <UserAvatar :src="reply.avatar" :size="28" :alt="commentAuthor(reply)" />
                    <div class="reply__body">
                      <div class="reply__head">
                        <span class="reply__author">{{ commentAuthor(reply) }}</span>
                        <span v-if="reply.reply_to_nickname" class="reply__to">
                          回复 @{{ reply.reply_to_nickname }}
                        </span>
                      </div>
                      <p class="reply__text">{{ reply.content }}</p>
                      <div class="comment__foot">
                        <span class="comment__time">{{ formatRelativeDate(reply.created_at) }}</span>
                        <button
                          class="comment__like"
                          :class="{ 'is-liked': reply.liked }"
                          type="button"
                          :disabled="commentLikePendingId === reply.id"
                          :aria-pressed="reply.liked"
                          :aria-label="reply.liked ? '取消点赞' : '点赞'"
                          @click="toggleCommentLike(reply)"
                        >
                          <AppIcon name="thumbs-up" :size="16" />
                          <span v-if="reply.like_count > 0" class="cy-num">
                            {{ formatCount(reply.like_count) }}
                          </span>
                        </button>
                        <button
                          class="comment__reply-btn"
                          :class="{ 'is-active': isReplying(reply) }"
                          type="button"
                          aria-label="回复"
                          @click="toggleReply(item, reply)"
                        >
                          回复
                        </button>
                        <button
                          v-if="canDeleteComment(reply)"
                          class="comment__delete"
                          type="button"
                          aria-label="删除回复"
                          @click="askDeleteComment(reply)"
                        >
                          <AppIcon name="trash" :size="16" />
                        </button>
                      </div>
                    </div>
                  </div>

                  <p v-if="replyExpandError" class="cy-alert cy-alert--error reply-list__error">
                    {{ replyExpandError }}
                  </p>
                  <button
                    v-if="item.reply_count > item.replies.length"
                    class="reply-list__more"
                    type="button"
                    :disabled="replyExpandLoadingId === item.id"
                    @click="expandReplies(item)"
                  >
                    {{
                      replyExpandLoadingId === item.id
                        ? '加载中…'
                        : `展开更多回复（${item.reply_count - item.replies.length}）`
                    }}
                  </button>
                </div>

                <!-- 内联回复框：点某条评论 / 回复的「回复」后在该一级评论下展开 -->
                <div v-if="showReplyBox(item)" class="reply-box">
                  <textarea
                    v-model="replyDraft"
                    class="reply-box__input"
                    rows="2"
                    maxlength="1000"
                    :placeholder="replyPlaceholder()"
                    :disabled="replySubmitting"
                  />
                  <p v-if="replySubmitError" class="cy-alert cy-alert--error reply-box__error">
                    {{ replySubmitError }}
                  </p>
                  <div class="reply-box__foot">
                    <button
                      class="cy-btn cy-btn--secondary cy-btn--pill"
                      type="button"
                      :disabled="replySubmitting"
                      @click="closeReply"
                    >
                      取消
                    </button>
                    <button
                      class="cy-btn cy-btn--primary cy-btn--pill"
                      type="button"
                      :disabled="replySubmitting || !replyDraft.trim()"
                      @click="submitReply(item)"
                    >
                      {{ replySubmitting ? '发送中…' : '回复' }}
                    </button>
                  </div>
                </div>
              </div>
            </article>

            <div class="comment-list__more">
              <div v-if="commentMoreError" class="cy-alert cy-alert--error comment__empty">
                {{ commentMoreError }}
              </div>
              <button
                v-if="hasMoreComments"
                class="cy-btn cy-btn--secondary cy-btn--pill"
                type="button"
                :disabled="commentLoadingMore"
                @click="loadComments(false)"
              >
                {{ commentLoadingMore ? '加载中…' : '加载更多评论' }}
              </button>
            </div>
          </div>
        </section>
      </div>

      <aside class="detail__side">
        <!-- 合集卡片：只在接口返回非空时出现，当前视频所在的集数直接标在卡片上 -->
        <section
          v-if="videoCollections.length > 0"
          class="side-collections"
          aria-labelledby="side-collections-title"
        >
          <h2
            id="side-collections-title"
            class="cy-subtitle detail__side-title side-collections__title"
          >
            合集
          </h2>

          <RouterLink
            v-for="collection in videoCollections"
            :key="collection.id"
            class="side-collection"
            :to="{ name: 'collection', params: { id: collection.id } }"
          >
            <div class="side-collection__cover">
              <img
                v-if="collection.cover_url"
                class="side-collection__img"
                :src="collection.cover_url"
                :alt="collection.title"
                loading="lazy"
              />
              <span v-else class="side-collection__fallback" aria-hidden="true">
                {{ initialOf(collection.title) }}
              </span>
            </div>

            <div class="side-collection__info">
              <h3 class="side-collection__name cy-clamp-2">{{ collection.title }}</h3>
              <p class="cy-caption">
                共 <span class="cy-num">{{ collection.video_count }}</span> 个视频
              </p>
              <span class="side-collection__index">
                <AppIcon name="play" :size="12" />
                第 <span class="cy-num">{{ collection.current_index }}</span> 集
              </span>
            </div>
          </RouterLink>
        </section>

        <div class="detail__side-head">
          <h2 class="cy-subtitle detail__side-title">播放队列</h2>
          <button
            class="queue-auto"
            :class="{ 'is-on': queueStore.autoPlay }"
            type="button"
            role="switch"
            :aria-checked="queueStore.autoPlay"
            @click="queueStore.autoPlay = !queueStore.autoPlay"
          >
            自动连播
          </button>
        </div>

        <div v-if="relatedLoading" class="detail__side-list">
          <div v-for="index in 4" :key="index" class="detail__skeleton-side cy-skeleton" />
        </div>

        <p v-else-if="queueStore.items.length === 0" class="cy-caption">暂无更多内容</p>

        <div v-else class="detail__side-list">
          <div
            v-for="item in queueStore.items"
            :key="item.id"
            class="related"
            :class="{ 'is-current': item.id === videoId }"
          >
            <RouterLink
              class="related__link"
              :to="{ name: 'video-detail', params: { id: item.id } }"
            >
              <div class="related__cover">
                <img
                  v-if="item.cover_url"
                  class="related__img"
                  :src="item.cover_url"
                  :alt="item.title"
                  loading="lazy"
                />
                <span v-else class="related__fallback" aria-hidden="true">
                  {{ initialOf(item.title) }}
                </span>
                <span v-if="item.duration > 0" class="related__duration cy-num">
                  {{ formatDuration(item.duration) }}
                </span>
              </div>
              <div class="related__info">
                <h3 class="related__title cy-clamp-2">{{ item.title }}</h3>
                <p class="cy-caption">
                  <span v-if="item.id === videoId" class="related__playing">正在播放</span>
                  <template v-else>
                    窗月官方 · <span class="cy-num">{{ formatCount(item.view_count) }}</span> 次播放
                  </template>
                </p>
              </div>
            </RouterLink>
            <button
              v-if="item.id !== videoId"
              class="related__remove"
              type="button"
              :aria-label="`从队列移除 ${item.title}`"
              @click="queueStore.remove(item.id)"
            >
              <AppIcon name="close" :size="14" />
            </button>
          </div>
        </div>
      </aside>
    </div>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除这条评论？"
      description="删除后这条评论将不再展示。"
      confirm-label="删除"
      danger
      :busy="deleteBusy"
      :error="deleteError"
      @close="closeDeleteComment"
      @confirm="confirmDeleteComment"
    />

    <ConfirmDialog
      :open="danmakuDeleteTarget !== null"
      title="删除这条弹幕？"
      description="删除后这条弹幕将不再出现在播放器上。"
      confirm-label="删除"
      danger
      :busy="danmakuDeleteBusy"
      :error="danmakuDeleteError"
      @close="closeDeleteDanmaku"
      @confirm="confirmDeleteDanmaku"
    />

    <ReportDialog
      :open="reportOpen"
      :target-type="reportTargetType"
      :target-id="reportTargetId"
      @close="closeReport"
      @submitted="onReportSubmitted"
    />
  </div>
</template>

<style scoped>
.detail {
  padding-top: var(--cy-space-5);
  padding-bottom: var(--cy-space-7);
}

.detail__layout {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 360px;
  gap: var(--cy-space-6);
  align-items: start;
}

.detail__main {
  min-width: 0;
}

/* 播放器：16:9，圆角 16px，纯黑底，无阴影 */
.player {
  position: relative;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-lg);
  overflow: hidden;
  background: var(--cy-player-bg);
}

.player__video {
  width: 100%;
  height: 100%;
  object-fit: contain;
  background: var(--cy-player-bg);
}

.player__empty {
  display: grid;
  place-items: center;
  height: 100%;
  color: var(--cy-media-badge-text);
  font-size: 14px;
  opacity: 0.7;
}

.player__loading {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  pointer-events: none;
}

/* ===== 自建播放控制条 ===== */

/* 控制条淡出后连鼠标一起隐藏，避免在画面上留一个箭头 */
.player.is-idle {
  cursor: none;
}

/* 全屏时不保留圆角，画面顶满 */
.player:fullscreen {
  border-radius: 0;
}

/*
 * 网页全屏（剧场）：播放器脱离文档流铺满视口，盖住顶栏与左导航（z-index 高于顶栏的 100/120），
 * 但低于各类弹框（200+），所以全屏状态下打开确认框仍能正常浮在画面之上。
 */
.player.is-web-fullscreen {
  position: fixed;
  inset: 0;
  z-index: 150;
  aspect-ratio: auto;
  border-radius: 0;
}

.pc {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  z-index: 4;
  padding: var(--cy-space-4) var(--cy-space-4) var(--cy-space-2);
  background: linear-gradient(to top, rgba(0, 0, 0, 0.72), rgba(0, 0, 0, 0));
  color: #fff;
  transition: opacity var(--cy-dur) var(--cy-ease);
}

.pc.is-hidden {
  opacity: 0;
  pointer-events: none;
}

/* 进度条：比原生更细，悬停时加粗并露出圆点把手 */
.pc__progress {
  padding: var(--cy-space-2) 0;
  cursor: pointer;
  touch-action: none;
}

.pc__progress-track {
  position: relative;
  height: 4px;
  border-radius: var(--cy-radius-full);
  background: rgba(255, 255, 255, 0.28);
  transition: height var(--cy-dur-fast) var(--cy-ease);
}

.pc__progress:hover .pc__progress-track {
  height: 6px;
}

.pc__progress-buffered,
.pc__progress-played {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  border-radius: var(--cy-radius-full);
}

.pc__progress-buffered {
  background: rgba(255, 255, 255, 0.42);
}

.pc__progress-played {
  background: var(--cy-primary);
}

.pc__progress-knob {
  position: absolute;
  top: 50%;
  right: 0;
  width: 12px;
  height: 12px;
  border-radius: var(--cy-radius-full);
  background: #fff;
  transform: translate(50%, -50%) scale(0);
  transition: transform var(--cy-dur-fast) var(--cy-ease);
}

.pc__progress:hover .pc__progress-knob {
  transform: translate(50%, -50%) scale(1);
}

.pc__bar {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-2);
}

.pc__btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  color: #fff;
  border-radius: var(--cy-radius-full);
  transition: background-color var(--cy-dur-fast) var(--cy-ease);
}

.pc__btn:hover {
  background: rgba(255, 255, 255, 0.16);
}

/* 带文字的按钮（当前用于清晰度）：宽度随内容，图标与档位文案并排 */
.pc__btn--text {
  width: auto;
  padding: 0 var(--cy-space-2);
  gap: 4px;
}

.pc__quality-label {
  color: rgba(255, 255, 255, 0.9);
  font-size: 13px;
  line-height: 18px;
  white-space: nowrap;
}

.pc__time {
  color: rgba(255, 255, 255, 0.9);
  font-size: 13px;
  line-height: 18px;
  white-space: nowrap;
}

.pc__spacer {
  flex: 1;
}

/* 音量：按钮点一下静音；悬停时向上展开竖直滑条 */
.pc__volume {
  position: relative;
  display: inline-flex;
}

/* 按钮与面板之间的隐形过渡区，保证鼠标从按钮上移到面板时不丢 hover */
.pc__volume::after {
  content: '';
  position: absolute;
  left: 50%;
  bottom: 100%;
  width: 32px;
  height: var(--cy-space-2);
  transform: translateX(-50%);
}

/* 竖直音量：面板只做定位与悬停热区，不画背景框 */
.pc__volume-panel {
  position: absolute;
  bottom: calc(100% + var(--cy-space-2));
  left: 50%;
  width: 32px;
  height: 92px;
  transform: translateX(-50%);
  visibility: hidden;
  opacity: 0;
  transition: opacity var(--cy-dur-fast) var(--cy-ease);
}

.pc__volume:hover .pc__volume-panel,
.pc__volume:focus-within .pc__volume-panel {
  visibility: visible;
  opacity: 1;
}

/*
 * 竖直音量条：滑条本体旋转 -90°，轨道与填充都由 input 自身的 runnable-track 绘制，
 * 与圆点把手同源，位置天然对齐、不会出现圆点压条或错位。
 * 旋转后「向右」即「向上」，所以渐变用 to right。
 */
.pc__volume-slider {
  position: absolute;
  top: 50%;
  left: 50%;
  width: 76px;
  height: 20px;
  margin: 0;
  padding: 0;
  appearance: none;
  -webkit-appearance: none;
  background: transparent;
  outline: none;
  cursor: pointer;
  transform: translate(-50%, -50%) rotate(-90deg);
}

.pc__volume-slider::-webkit-slider-runnable-track {
  height: 4px;
  border-radius: var(--cy-radius-full);
  background: linear-gradient(
    to right,
    var(--cy-primary) var(--volume-pct, 100%),
    rgba(255, 255, 255, 0.32) var(--volume-pct, 100%)
  );
}

.pc__volume-slider::-webkit-slider-thumb {
  -webkit-appearance: none;
  width: 12px;
  height: 12px;
  margin-top: -4px;
  border-radius: var(--cy-radius-full);
  background: #fff;
  cursor: pointer;
}

.pc__volume-slider::-moz-range-track {
  height: 4px;
  border-radius: var(--cy-radius-full);
  background: linear-gradient(
    to right,
    var(--cy-primary) var(--volume-pct, 100%),
    rgba(255, 255, 255, 0.32) var(--volume-pct, 100%)
  );
}

.pc__volume-slider::-moz-range-thumb {
  width: 12px;
  height: 12px;
  border: none;
  border-radius: var(--cy-radius-full);
  background: #fff;
  cursor: pointer;
}

/* 倍速 */
.pc__rate {
  position: relative;
  display: inline-flex;
}

.pc__rate-menu {
  position: absolute;
  bottom: calc(100% + var(--cy-space-2));
  left: 50%;
  display: flex;
  flex-direction: column;
  min-width: 84px;
  padding: var(--cy-space-1);
  border-radius: var(--cy-radius-md);
  background: rgba(0, 0, 0, 0.86);
  transform: translateX(-50%);
}

.pc__rate-item {
  height: 30px;
  padding: 0 var(--cy-space-3);
  color: rgba(255, 255, 255, 0.86);
  font-size: 13px;
  text-align: left;
  border-radius: var(--cy-radius-sm);
  transition: background-color var(--cy-dur-fast) var(--cy-ease);
}

.pc__rate-item:hover {
  background: rgba(255, 255, 255, 0.14);
}

.pc__rate-item.is-active {
  color: var(--cy-primary);
  font-weight: 600;
}

/* 静音兜底提示：自动播放被拦下时给用户一个显式的开启声音入口 */
.pc__unmute {
  position: absolute;
  top: var(--cy-space-4);
  left: var(--cy-space-4);
  z-index: 5;
  display: inline-flex;
  align-items: center;
  gap: var(--cy-space-2);
  height: 32px;
  padding: 0 var(--cy-space-3);
  color: #fff;
  font-size: 13px;
  border-radius: var(--cy-radius-full);
  background: rgba(0, 0, 0, 0.66);
  transition: background-color var(--cy-dur-fast) var(--cy-ease);
}

.pc__unmute:hover {
  background: rgba(0, 0, 0, 0.82);
}

.detail__title {
  margin-top: var(--cy-space-5);
}

.detail__meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--cy-space-4);
  margin-top: var(--cy-space-3);
  color: var(--cy-text-muted);
  font-size: 13px;
  line-height: 20px;
}

/* 点赞 / 收藏：圆形图标按钮 + 计数，两组之间留出比弹幕工具条更宽的间距 */
.detail__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--cy-space-5);
  margin-top: var(--cy-space-4);
}

.detail__action {
  display: inline-flex;
  align-items: center;
  gap: var(--cy-space-2);
}

.detail__action-btn {
  width: 44px;
  height: 44px;
}

.detail__action-count {
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 20px;
}

.detail__action-error {
  margin-top: var(--cy-space-3);
}

/* 视频级「举报」：图标按钮，靠操作组右端对齐，hover 才浮现底色，不与点赞 / 收藏抢视觉 */
.detail__report {
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  margin-left: auto;
  border-radius: var(--cy-radius-full);
  color: var(--cy-text-muted);
  transition: color var(--cy-dur-fast) var(--cy-ease),
    background-color var(--cy-dur-fast) var(--cy-ease);
}

.detail__report:hover {
  background: var(--cy-surface-2);
  color: var(--cy-accent);
}

.detail__report-success {
  margin-top: var(--cy-space-3);
}

/* 简介与评论区：不留卡片背景与内边距，正文与视频标题左边缘对齐 */
.detail__section {
  margin-top: var(--cy-space-5);
}

/* 评论区与简介之间用一条浅线分隔（参考 B 站），线上下各留出间距 */
.detail__section:not(.detail__section--desc) {
  padding-top: var(--cy-space-5);
  border-top: 1px solid var(--cy-border);
}

.detail__section-title {
  margin-bottom: var(--cy-space-4);
}

.detail__desc {
  margin-top: var(--cy-space-3);
  color: var(--cy-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
}

.detail__desc.is-collapsed {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 4;
  overflow: hidden;
}

.detail__toggle {
  margin-top: var(--cy-space-3);
  color: var(--cy-accent);
  font-size: 13px;
  line-height: 20px;
  font-weight: 500;
}

.detail__comment-count {
  margin-left: var(--cy-space-2);
  color: var(--cy-text-muted);
  font-size: 14px;
  font-weight: 400;
}

/* 发表评论：输入框 + 右下角提交按钮 */
.composer {
  margin-top: var(--cy-space-4);
}

.composer__input {
  width: 100%;
  padding: var(--cy-space-3) var(--cy-space-4);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-md);
  background: var(--cy-surface-2);
  color: var(--cy-text);
  font-family: inherit;
  font-size: 14px;
  line-height: 22px;
  resize: vertical;
  transition: border-color var(--cy-dur-fast) var(--cy-ease),
    box-shadow var(--cy-dur-fast) var(--cy-ease);
}

.composer__input:focus {
  outline: none;
  border-color: var(--cy-primary);
  box-shadow: 0 0 0 3px var(--cy-primary-soft);
}

/* 未聚焦输入框时不露出「发表评论」，点进输入框才显示（参考 B 站） */
.composer__foot {
  display: none;
  align-items: center;
  justify-content: flex-end;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-3);
}

.composer:focus-within .composer__foot {
  display: flex;
}

.composer__error {
  margin-top: var(--cy-space-3);
}

.comment-list {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-4);
  margin-top: var(--cy-space-5);
}

.comment {
  display: flex;
  gap: var(--cy-space-3);
}

.comment__body {
  min-width: 0;
  flex: 1;
}

.comment__head {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
}

.comment__author {
  font-size: 14px;
  line-height: 20px;
  font-weight: 600;
  color: var(--cy-text);
}

.comment__time {
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
}

.comment__delete {
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: var(--cy-radius-full);
  color: var(--cy-text-muted);
  transition: color var(--cy-dur-fast) var(--cy-ease),
    background-color var(--cy-dur-fast) var(--cy-ease);
}

.comment__delete:hover {
  background: var(--cy-surface-2);
  color: var(--cy-danger);
}

.comment__text {
  margin-top: var(--cy-space-1);
  color: var(--cy-text-secondary);
  font-size: 14px;
  line-height: 22px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

/* 评论底部：发布时间 + 点赞按钮，与评论正文左对齐 */
.comment__foot {
  display: flex;
  align-items: center;
  gap: var(--cy-space-4);
  margin-top: var(--cy-space-2);
}

.comment__like {
  display: inline-flex;
  align-items: center;
  gap: var(--cy-space-1);
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
  transition: color var(--cy-dur-fast) var(--cy-ease);
}

.comment__like:hover,
.comment__like.is-liked {
  color: var(--cy-primary);
}

.comment__like:disabled {
  opacity: 0.6;
}

/* 评论 / 回复上的「回复」文字按钮，与点赞按钮同款 */
.comment__reply-btn {
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
  font-weight: 500;
  transition: color var(--cy-dur-fast) var(--cy-ease);
}

.comment__reply-btn:hover,
.comment__reply-btn.is-active {
  color: var(--cy-primary);
}

/* 一级评论下的回复区：缩进一层，与一级评论正文区分 */
.reply-list {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-3);
}

.reply {
  display: flex;
  gap: var(--cy-space-3);
}

.reply__body {
  min-width: 0;
  flex: 1;
}

.reply__head {
  display: flex;
  align-items: baseline;
  gap: var(--cy-space-2);
  min-width: 0;
}

.reply__author {
  color: var(--cy-text);
  font-size: 13px;
  line-height: 20px;
  font-weight: 600;
}

/* 「回复 @某人」的被回复者标识 */
.reply__to {
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
}

.reply__text {
  margin-top: var(--cy-space-1);
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 21px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

/* 回复行底部沿用 .comment__foot，间距稍收，避免一行过长 */
.reply .comment__foot {
  gap: var(--cy-space-3);
}

.reply-list__error {
  margin-top: var(--cy-space-2);
}

/* 「展开更多回复」：文字按钮，不抢主视觉 */
.reply-list__more {
  align-self: flex-start;
  color: var(--cy-accent);
  font-size: 13px;
  line-height: 20px;
  font-weight: 500;
}

.reply-list__more:hover {
  text-decoration: underline;
}

.reply-list__more:disabled {
  opacity: 0.6;
}

/* 内联回复框：与一级评论正文左对齐，比主输入框更轻 */
.reply-box {
  margin-top: var(--cy-space-3);
}

.reply-box__input {
  width: 100%;
  padding: var(--cy-space-3) var(--cy-space-4);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-md);
  background: var(--cy-surface-2);
  color: var(--cy-text);
  font-family: inherit;
  font-size: 13px;
  line-height: 21px;
  resize: vertical;
  transition: border-color var(--cy-dur-fast) var(--cy-ease),
    box-shadow var(--cy-dur-fast) var(--cy-ease);
}

.reply-box__input:focus {
  outline: none;
  border-color: var(--cy-primary);
  box-shadow: 0 0 0 3px var(--cy-primary-soft);
}

.reply-box__error {
  margin-top: var(--cy-space-2);
}

.reply-box__foot {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-3);
}

.comment__like-error {
  margin-top: var(--cy-space-4);
}

.comment__empty {
  margin-top: var(--cy-space-4);
  text-align: center;
}

.comment__skeleton {
  height: 56px;
  border-radius: var(--cy-radius-sm);
}

.comment-list__more {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--cy-space-3);
}

/* 合集卡片：与播放队列共用侧栏宽度，封面比队列项略窄，给集数徽标留出空间 */
.side-collections {
  margin-bottom: var(--cy-space-5);
}

.side-collections__title {
  margin-bottom: var(--cy-space-3);
}

.side-collection {
  display: flex;
  gap: var(--cy-space-3);
  padding: var(--cy-space-1);
  border-radius: var(--cy-radius-sm);
  transition: background-color var(--cy-dur-fast) var(--cy-ease);
}

.side-collection + .side-collection {
  margin-top: var(--cy-space-2);
}

.side-collection:hover {
  background: var(--cy-surface-2);
}

.side-collection__cover {
  position: relative;
  flex-shrink: 0;
  width: 120px;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-sm);
  overflow: hidden;
  background: var(--cy-surface-2);
}

.side-collection__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.side-collection__fallback {
  display: grid;
  place-items: center;
  width: 100%;
  height: 100%;
  background: var(--cy-primary);
  color: var(--cy-on-primary);
  font-size: 18px;
  font-weight: 600;
}

.side-collection__info {
  min-width: 0;
}

.side-collection__name {
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
  font-weight: 500;
}

/* 集数徽标：浅紫底强调「当前视频在这里」，与队列的「正在播放」呼应 */
.side-collection__index {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  height: 20px;
  margin-top: var(--cy-space-1);
  padding: 0 var(--cy-space-2);
  border-radius: var(--cy-radius-full);
  background: var(--cy-primary-soft);
  color: var(--cy-primary-strong);
  font-size: 12px;
  line-height: 20px;
  font-weight: 600;
}

.detail__side-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--cy-space-3);
  margin-bottom: var(--cy-space-3);
}

.detail__side-title {
  margin-bottom: 0;
}

/* 自动连播开关：胶囊按钮，关闭时低调、开启时星紫 */
.queue-auto {
  flex-shrink: 0;
  padding: 2px var(--cy-space-3);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-full);
  background: var(--cy-surface-2);
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 20px;
  cursor: pointer;
  transition:
    color var(--cy-dur-fast) var(--cy-ease),
    border-color var(--cy-dur-fast) var(--cy-ease),
    background-color var(--cy-dur-fast) var(--cy-ease);
}

.queue-auto.is-on {
  border-color: var(--cy-primary);
  background: var(--cy-primary-soft);
  color: var(--cy-primary);
}

.detail__side-list {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-3);
}

.related {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  padding: var(--cy-space-1);
  border-radius: var(--cy-radius-sm);
  transition: background-color var(--cy-dur-fast) var(--cy-ease);
}

.related:hover {
  background: var(--cy-surface-2);
}

/* 正在播放的那一条：浅紫底做出「当前」的位置感 */
.related.is-current {
  background: var(--cy-primary-soft);
}

.related__link {
  display: flex;
  flex: 1;
  gap: var(--cy-space-3);
  min-width: 0;
}

.related__playing {
  font-weight: 600;
  color: var(--cy-primary);
}

/* 从队列移除：悬停或键盘聚焦时才出现，避免列表过于嘈杂 */
.related__remove {
  flex-shrink: 0;
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border: 0;
  border-radius: var(--cy-radius-full);
  background: transparent;
  color: var(--cy-text-muted);
  cursor: pointer;
  opacity: 0;
  transition:
    opacity var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease),
    background-color var(--cy-dur-fast) var(--cy-ease);
}

.related:hover .related__remove,
.related__remove:focus-visible {
  opacity: 1;
}

.related__remove:hover {
  background: var(--cy-border);
  color: var(--cy-text);
}

.related__cover {
  position: relative;
  width: 160px;
  flex-shrink: 0;
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-sm);
  overflow: hidden;
  background: var(--cy-surface-2);
}

.related__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.related__fallback {
  display: grid;
  place-items: center;
  width: 100%;
  height: 100%;
  background: var(--cy-primary);
  color: var(--cy-on-primary);
  font-size: 24px;
  font-weight: 600;
}

.related__duration {
  position: absolute;
  right: var(--cy-space-1);
  bottom: var(--cy-space-1);
  height: 18px;
  padding: 0 var(--cy-space-1);
  border-radius: var(--cy-radius-xs);
  background: var(--cy-media-badge-bg);
  color: var(--cy-media-badge-text);
  font-size: 12px;
  line-height: 18px;
}

.related__info {
  min-width: 0;
}

.related__title {
  font-size: 14px;
  line-height: 20px;
  font-weight: 500;
  color: var(--cy-text);
}

.detail__skeleton-player {
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-lg);
}

.detail__skeleton-title {
  height: 28px;
  margin-top: var(--cy-space-5);
  border-radius: var(--cy-radius-sm);
}

.detail__skeleton-line {
  height: 14px;
  width: 40%;
  margin-top: var(--cy-space-4);
  border-radius: var(--cy-radius-sm);
}

.detail__skeleton-side-title {
  height: 20px;
  width: 40%;
  margin-bottom: var(--cy-space-3);
  border-radius: var(--cy-radius-sm);
}

.detail__skeleton-side {
  height: 90px;
  border-radius: var(--cy-radius-sm);
}

.detail__skeleton-side + .detail__skeleton-side {
  margin-top: var(--cy-space-3);
}

@media (min-width: 1024px) {
  .detail__side {
    position: sticky;
    top: calc(var(--cy-header-h) + var(--cy-space-5));
    max-height: calc(100vh - var(--cy-header-h) - var(--cy-space-6));
    overflow-y: auto;
    overscroll-behavior: contain;
  }
}

@media (max-width: 1023px) {
  .detail__layout {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 599px) {
  .related__cover {
    width: 128px;
  }

  .side-collection__cover {
    width: 104px;
  }
}

/* 弹幕层：铺满播放器但不吃鼠标事件，只有弹幕本身可点 */
.danmaku {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
  font-size: 20px;
  font-weight: 600;
  line-height: 1.2;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.85);
}

.danmaku__item {
  position: absolute;
  white-space: nowrap;
  will-change: transform;
  cursor: default;
}

/* 滚动弹幕：从右向左推进，位移由脚本按视频进度算好后写在行内 transform 上 */
.danmaku__item--1 {
  left: 0;
}

/* 顶部 / 底部弹幕：水平居中停留 */
.danmaku__item--2,
.danmaku__item--3 {
  left: 50%;
  transform: translateX(-50%);
}

.danmaku__item.is-deletable {
  pointer-events: auto;
  cursor: pointer;
}

/* 弹幕工具条：贴在播放器下方 */
.danmaku-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--cy-space-3);
  margin-top: var(--cy-space-3);
}

.danmaku-bar__toggle {
  display: inline-flex;
  align-items: center;
  gap: var(--cy-space-1);
  padding: var(--cy-space-2) var(--cy-space-3);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-full);
  background: transparent;
  color: var(--cy-primary);
  font-size: 13px;
  cursor: pointer;
  transition: border-color 0.15s ease, color 0.15s ease;
}

.danmaku-bar__toggle:hover {
  border-color: var(--cy-primary);
}

.danmaku-bar__toggle.is-off {
  color: var(--cy-text-muted);
}

/* 弹幕设置：齿轮按钮 + 向上展开的设置面板 */
.danmaku-bar__settings {
  position: relative;
  display: inline-flex;
}

.danmaku-bar__settings-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-full);
  color: var(--cy-text-secondary);
  transition: border-color var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease);
}

.danmaku-bar__settings-trigger:hover,
.danmaku-bar__settings-trigger.is-open {
  border-color: var(--cy-primary);
  color: var(--cy-primary);
}

/* 弹幕关闭时设置不可修改：按钮变灰并禁用交互 */
.danmaku-bar__settings-trigger:disabled {
  color: var(--cy-text-muted);
  opacity: 0.5;
  cursor: not-allowed;
}

.danmaku-bar__settings-trigger:disabled:hover {
  border-color: var(--cy-border);
  color: var(--cy-text-muted);
}

/* 面板锚在齿轮按钮正上方，左对齐 */
.danmaku-settings {
  position: absolute;
  bottom: calc(100% + var(--cy-space-2));
  left: 0;
  z-index: 3;
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-3);
  width: 300px;
  max-width: 80vw;
  padding: var(--cy-space-4);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-md);
  background: var(--cy-pop-bg);
  box-shadow: var(--cy-shadow-pop);
}

.danmaku-settings__row {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
}

.danmaku-settings__label {
  flex: none;
  width: 64px;
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 20px;
}

.danmaku-settings__range {
  flex: 1;
  min-width: 0;
  accent-color: var(--cy-primary);
}

.danmaku-settings__value {
  flex: none;
  width: 42px;
  text-align: right;
  color: var(--cy-text-secondary);
  font-size: 12px;
  line-height: 18px;
}

.danmaku-settings__options {
  display: inline-flex;
  gap: var(--cy-space-2);
}

.danmaku-settings__chip {
  min-width: 44px;
  height: 28px;
  padding: 0 var(--cy-space-3);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-full);
  color: var(--cy-text-secondary);
  font-size: 13px;
  transition: border-color var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease),
    background-color var(--cy-dur-fast) var(--cy-ease);
}

.danmaku-settings__chip:hover {
  border-color: var(--cy-primary);
}

.danmaku-settings__chip.is-active {
  border-color: var(--cy-primary);
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

/* 颜色：圆形色块，选中时星紫外圈 */
.danmaku-settings__swatch {
  width: 22px;
  height: 22px;
  padding: 0;
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-full);
  cursor: pointer;
  transition: box-shadow var(--cy-dur-fast) var(--cy-ease);
}

.danmaku-settings__swatch.is-active {
  outline: 2px solid var(--cy-primary);
  outline-offset: 2px;
}

.danmaku-settings__reset {
  align-self: flex-start;
  color: var(--cy-accent);
  font-size: 13px;
  line-height: 20px;
  font-weight: 500;
}

.danmaku-bar__input {
  flex: 1;
  min-width: 160px;
  height: 38px;
  padding: 0 var(--cy-space-4);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-full);
  background: var(--cy-surface);
  color: var(--cy-text);
  font-size: 14px;
}

.danmaku-bar__input:focus {
  outline: none;
  border-color: var(--cy-primary);
}

.danmaku-bar__send {
  flex: none;
}

.danmaku-bar__error {
  margin-top: var(--cy-space-2);
}
</style>
