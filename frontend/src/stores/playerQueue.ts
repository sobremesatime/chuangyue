// 播放队列：观看页右侧列表的数据源，也是「自动连播」的依据。
//
// 队列整体存进 localStorage，跨路由与刷新都保留。进入某个视频时，
// 若它不在队列里（说明是从队列外的入口点进来的，例如首页或收藏页），
// 就用「当前视频 + 同分类内容」重建队列，保证任何入口都有可连播的内容；
// 若它已在队列里（说明是顺着队列看下来的），则保持队列不变，位置也不丢。

import { ref, watch } from 'vue'
import { defineStore } from 'pinia'

import type { Video } from '@/types/api'

/** 队列在 localStorage 里的键名 */
const QUEUE_KEY = 'chuangyue:player-queue'

/** 自动连播开关在 localStorage 里的键名 */
const AUTOPLAY_KEY = 'chuangyue:player-autoplay'

/** 队列上限，避免长期使用后本地存储里越堆越多 */
const QUEUE_MAX = 50

/** 读取本地队列：解析失败或结构不对一律回落为空队列，不让脏数据影响观看 */
function loadItems(): Video[] {
  try {
    const raw = localStorage.getItem(QUEUE_KEY)
    if (!raw) {
      return []
    }
    const parsed = JSON.parse(raw) as unknown
    if (!Array.isArray(parsed)) {
      return []
    }
    return parsed
      .filter(
        (item): item is Video =>
          !!item && typeof item === 'object' && typeof (item as Video).id === 'string',
      )
      .slice(0, QUEUE_MAX)
  } catch {
    return []
  }
}

/** 读取自动连播开关：默认开启 */
function loadAutoPlay(): boolean {
  try {
    return localStorage.getItem(AUTOPLAY_KEY) !== '0'
  } catch {
    return true
  }
}

/** 写入本地存储：隐私模式等写不进去时忽略，队列仅在本次会话生效 */
function persist(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    // 存不进去就算了
  }
}

export const usePlayerQueueStore = defineStore('playerQueue', () => {
  const items = ref<Video[]>(loadItems())
  const autoPlay = ref(loadAutoPlay())

  watch(items, (value) => persist(QUEUE_KEY, JSON.stringify(value)))
  watch(autoPlay, (value) => persist(AUTOPLAY_KEY, value ? '1' : '0'))

  /** 用给定列表整体替换队列（进入队列外的视频时重建） */
  function setQueue(list: Video[]): void {
    items.value = list.slice(0, QUEUE_MAX)
  }

  /** 把若干视频追加进队列，按 id 去重，返回实际新增的条数 */
  function enqueue(list: Video[]): number {
    const seen = new Set(items.value.map((item) => item.id))
    const added: Video[] = []
    for (const item of list) {
      if (seen.has(item.id)) {
        continue
      }
      seen.add(item.id)
      added.push(item)
    }
    if (added.length > 0) {
      items.value = [...items.value, ...added].slice(0, QUEUE_MAX)
    }
    return added.length
  }

  /** 从队列中移除一条 */
  function remove(id: string): void {
    items.value = items.value.filter((item) => item.id !== id)
  }

  function has(id: string): boolean {
    return items.value.some((item) => item.id === id)
  }

  /** 队列中紧跟在 id 之后的条目，用于播完自动连播；没有下一条时返回 null */
  function nextAfter(id: string): Video | null {
    const index = items.value.findIndex((item) => item.id === id)
    if (index < 0 || index + 1 >= items.value.length) {
      return null
    }
    return items.value[index + 1] ?? null
  }

  return { items, autoPlay, setQueue, enqueue, remove, has, nextAfter }
})
