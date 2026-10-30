// axios 实例与拦截器：所有与后端的交互都从这里走。
//
// 约定（与后端 pkg/response 对应）：
//   - 后端统一返回 { code, message, data, trace_id }，code 为 0 表示成功；
//   - 访问令牌放在 Authorization: Bearer <token>；
//   - 刷新令牌由后端写入 HttpOnly Cookie，前端读不到也不需要读；
//   - 访问令牌过期（HTTP 401）时自动刷新一次并重放原请求，业务代码无感知。

import axios, { AxiosError, type AxiosInstance, type InternalAxiosRequestConfig } from 'axios'

import type { ApiEnvelope, TokenPair } from '@/types/api'

/** 网关地址：开发环境由 Vite proxy 转发，生产环境由 Nginx 同域反代 */
const BASE_URL = import.meta.env.VITE_API_BASE_URL || '/api/v1'

/** 开发环境：保留原始错误信息，方便定位问题 */
const isDev = import.meta.env.DEV

/** 业务错误：统一携带错误码与提示信息，页面直接展示 message 即可 */
export class ApiError extends Error {
  readonly code: number
  /**
   * 原始 HTTP 状态码（0 表示请求未到达服务端）。
   *
   * 必须与业务 code 分开存：业务码是五位（如 20002 密码错误），
   * 单看数值会把「密码错误」误判成 5xx 服务故障。
   */
  readonly httpStatus: number

  constructor(code: number, message: string, httpStatus = 0) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.httpStatus = httpStatus
  }
}

/**
 * 访问令牌只保存在内存中，不写入 localStorage。
 * 页面刷新后由 Pinia 的 bootstrap() 用刷新令牌重新换取，
 * 这样即使发生 XSS 也拿不到长期凭证。
 */
let accessToken = ''

export function setAccessToken(token: string): void {
  accessToken = token
}

export function clearAccessToken(): void {
  accessToken = ''
}

export const http: AxiosInstance = axios.create({
  baseURL: BASE_URL,
  timeout: 15000,
  // 必须带上 Cookie，刷新令牌才会随请求发送
  withCredentials: true,
})

http.interceptors.request.use((config) => {
  if (accessToken) {
    config.headers.Authorization = `Bearer ${accessToken}`
  }
  return config
})

/** 标记请求是否已经重放过，避免无限重试 */
type RetriableConfig = InternalAxiosRequestConfig & { _retried?: boolean }

/** 并发去重：多个请求同时 401 时，只发起一次刷新 */
let refreshPromise: Promise<string> | null = null

function refreshAccessToken(): Promise<string> {
  if (!refreshPromise) {
    refreshPromise = axios
      .post<ApiEnvelope<TokenPair>>(`${BASE_URL}/auth/refresh`, null, { withCredentials: true })
      .then((res) => {
        accessToken = res.data.data.access_token
        return accessToken
      })
      .finally(() => {
        refreshPromise = null
      })
  }
  return refreshPromise
}

/** 认证接口自身不触发自动刷新，避免死循环 */
function isAuthEndpoint(url?: string): boolean {
  if (!url) {
    return false
  }
  return (
    url.includes('/auth/login') ||
    url.includes('/auth/register') ||
    url.includes('/auth/refresh') ||
    url.includes('/auth/email/code')
  )
}

http.interceptors.response.use(
  (response) => {
    const body = response.data as ApiEnvelope | undefined
    // 业务失败（HTTP 200 但 code 非 0）统一转成 ApiError
    if (body && typeof body.code === 'number' && body.code !== 0) {
      return Promise.reject(new ApiError(body.code, body.message || '请求失败'))
    }
    return response
  },
  async (error: AxiosError<ApiEnvelope>) => {
    const response = error.response
    const config = error.config as RetriableConfig | undefined

    if (!response) {
      return Promise.reject(new ApiError(-1, '网络异常，请确认后端网关已启动'))
    }

    // 访问令牌过期：静默刷新后重放原请求
    if (response.status === 401 && config && !config._retried && !isAuthEndpoint(config.url)) {
      config._retried = true
      try {
        const token = await refreshAccessToken()
        config.headers.Authorization = `Bearer ${token}`
        return await http.request(config)
      } catch {
        clearAccessToken()
        return Promise.reject(
          new ApiError(response.status, response.data?.message || '登录已过期，请重新登录', response.status),
        )
      }
    }

    const message = response.data?.message || error.message || '请求失败'
    return Promise.reject(
      new ApiError(response.data?.code ?? response.status, message, response.status),
    )
  },
)

/**
 * 把任意异常转成可展示的文案。
 *
 * 面向前端用户时不给技术细节：网络中断、服务端异常这类只给友好说法，
 * 业务错误（登录失败、用户名被占用等）则原样展示后端给的中文提示。
 * 开发环境保留原始信息，方便定位。
 */
export function toErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === -1) {
      return isDev ? err.message : '网络连接异常，请稍后重试'
    }
    if (err.httpStatus >= 500) {
      return isDev ? err.message : '服务暂时不可用，请稍后重试'
    }
    return err.message
  }
  if (err instanceof Error) {
    return isDev ? err.message : '操作失败，请稍后重试'
  }
  return '操作失败，请稍后重试'
}
