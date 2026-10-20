// 明暗模式状态。
//
// 默认跟随系统偏好；用户在顶栏手动切换后写入 localStorage，并优先于系统偏好。
// 解析结果写到 <html data-theme>，CSS 的令牌表据此整体换肤。
// 注意：index.html 里有一段同逻辑的内联脚本，用于首屏渲染前就定好皮肤。

import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

export type ThemeMode = 'system' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

const STORAGE_KEY = 'cy-theme'
const DARK_QUERY = '(prefers-color-scheme: dark)'

function readStoredMode(): ThemeMode {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === 'light' || raw === 'dark' || raw === 'system') {
      return raw
    }
  } catch {
    // 隐私模式下 localStorage 可能不可用，按「跟随系统」处理
  }
  return 'system'
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStoredMode())
  const systemDark = ref(
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia(DARK_QUERY).matches
      : true,
  )

  const resolved = computed<ResolvedTheme>(() =>
    mode.value === 'system' ? (systemDark.value ? 'dark' : 'light') : mode.value,
  )

  /** 把解析结果写到 <html>，CSS 令牌表据此切换 */
  function apply(): void {
    const root = document.documentElement
    root.dataset.theme = resolved.value
    root.style.colorScheme = resolved.value
  }

  function persist(): void {
    try {
      localStorage.setItem(STORAGE_KEY, mode.value)
    } catch {
      // 存不下就只在本次会话生效
    }
  }

  /** 在两个显式模式间切换（要回到「跟随系统」请用 setMode） */
  function toggle(): void {
    setMode(resolved.value === 'dark' ? 'light' : 'dark')
  }

  function setMode(next: ThemeMode): void {
    mode.value = next
    persist()
    apply()
  }

  /** 应用启动时调用一次：应用初始主题，并监听系统偏好变化 */
  function init(): void {
    apply()
    if (typeof window.matchMedia !== 'function') {
      return
    }
    window.matchMedia(DARK_QUERY).addEventListener('change', (event) => {
      systemDark.value = event.matches
      if (mode.value === 'system') {
        apply()
      }
    })
  }

  return { mode, resolved, init, toggle, setMode }
})
