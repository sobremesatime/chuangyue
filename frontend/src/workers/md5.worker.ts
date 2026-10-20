// 整文件 MD5 计算 Worker。
//
// 大文件不能整块读进内存（几百 MB 的 ArrayBuffer 很容易把页面撑爆），
// 所以这里按固定大小切片顺序读取，再增量喂给 spark-md5，
// 过程中把进度（0~100）回传主线程用于展示。
//
// 该文件由 UploadView 通过
//   new Worker(new URL('../workers/md5.worker.ts', import.meta.url), { type: 'module' })
// 创建，Vite 原生支持这种写法。

import SparkMD5 from 'spark-md5'

/** 每次读取的切片大小：2MB，兼顾读取次数与单块内存占用 */
const SLICE_SIZE = 2 * 1024 * 1024

interface Md5Request {
  file: File
}

/** 回传消息：进度 / 完成 / 失败三选一 */
export type Md5Response =
  | { type: 'progress'; percent: number }
  | { type: 'done'; md5: string }
  | { type: 'error'; message: string }

// DOM lib 下没有 WebWorker 的全局类型，self 会被推断成 Window（postMessage 需要 targetOrigin），
// 这里只声明 Worker 侧真正用到的最小接口，避免额外引入 lib。
interface WorkerScope {
  postMessage(message: Md5Response): void
  addEventListener(type: 'message', listener: (event: MessageEvent<Md5Request>) => void): void
}

const scope = self as unknown as WorkerScope

/** FileReader 的 Promise 包装，读取失败时抛出可读错误 */
function readSlice(blob: Blob): Promise<ArrayBuffer> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as ArrayBuffer)
    reader.onerror = () => reject(reader.error ?? new Error('读取文件失败'))
    reader.readAsArrayBuffer(blob)
  })
}

scope.addEventListener('message', (event) => {
  const file = event.data.file
  const spark = new SparkMD5.ArrayBuffer()
  const totalSlices = Math.max(1, Math.ceil(file.size / SLICE_SIZE))

  void (async () => {
    try {
      for (let index = 0; index < totalSlices; index += 1) {
        const start = index * SLICE_SIZE
        const end = Math.min(start + SLICE_SIZE, file.size)
        spark.append(await readSlice(file.slice(start, end)))
        scope.postMessage({
          type: 'progress',
          percent: Math.round(((index + 1) / totalSlices) * 100),
        })
      }
      scope.postMessage({ type: 'done', md5: spark.end() })
    } catch (e) {
      scope.postMessage({ type: 'error', message: e instanceof Error ? e.message : '计算文件指纹失败' })
    }
  })()
})