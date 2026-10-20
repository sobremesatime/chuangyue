// 展示层格式化工具。

/** 播放量 / 点赞数：用「万 / 亿」缩写，避免长数字撑破卡片 */
export function formatCount(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return '0'
  }
  if (value < 10000) {
    return String(value)
  }
  if (value < 100000000) {
    return `${trimZero(value / 10000)}万`
  }
  return `${trimZero(value / 100000000)}亿`
}

/** 时长：秒 -> mm:ss 或 hh:mm:ss */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return '00:00'
  }
  const total = Math.floor(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  return h > 0 ? `${pad(h)}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

/** 日期：ISO 字符串 -> YYYY-MM-DD */
export function formatDate(input?: string): string {
  if (!input) {
    return ''
  }
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

/**
 * 相对时间：刚刚 / 12分钟前 / 3小时前 / 3天前 / 2个月前，超过一年回到具体日期。
 * 视频站里「多久之前」比绝对日期更有意义。
 */
export function formatRelativeDate(input?: string): string {
  if (!input) {
    return ''
  }
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) {
    return ''
  }

  const diff = Date.now() - date.getTime()
  if (diff < 0) {
    return formatDate(input)
  }

  const minute = 60 * 1000
  const hour = 60 * minute
  const day = 24 * hour

  if (diff < minute) {
    return '刚刚'
  }
  if (diff < hour) {
    return `${Math.floor(diff / minute)}分钟前`
  }
  if (diff < day) {
    return `${Math.floor(diff / hour)}小时前`
  }
  if (diff < 30 * day) {
    return `${Math.floor(diff / day)}天前`
  }
  if (diff < 365 * day) {
    return `${Math.floor(diff / (30 * day))}个月前`
  }
  return formatDate(input)
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

function trimZero(n: number): string {
  return n.toFixed(1).replace(/\.0$/, '')
}
