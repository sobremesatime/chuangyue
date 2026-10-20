// 用户状态：登录态、当前用户资料、角色判断。
//
// 访问令牌存在 request.ts 的内存变量里，本 store 只负责业务状态，
// 并通过 bootstrap() 在应用启动时用刷新令牌恢复登录态。

import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import * as authApi from '@/api/auth'
import { clearAccessToken, setAccessToken } from '@/api/request'
import {
  ROLE_ADMIN,
  type ChangePasswordRequest,
  type RegisterRequest,
  type ResetPasswordRequest,
  type SendCodeResult,
  type UpdateProfileRequest,
  type User,
} from '@/types/api'

export const useUserStore = defineStore('user', () => {
  const profile = ref<User | null>(null)

  const isLoggedIn = computed(() => profile.value !== null)
  const isAdmin = computed(() => profile.value?.role === ROLE_ADMIN)

  /** 登录：账号可以是用户名或邮箱，拿到访问令牌后立即拉取用户资料 */
  async function login(account: string, password: string): Promise<void> {
    const pair = await authApi.login({ account, password })
    setAccessToken(pair.access_token)
    profile.value = await authApi.fetchProfile()
  }

  /** 注册：后端只创建普通用户，注册成功后由页面引导去登录 */
  async function register(payload: RegisterRequest): Promise<void> {
    await authApi.register(payload)
  }

  /** 发送注册邮箱验证码，返回的 cooldown 用于前端重发倒计时 */
  async function sendEmailCode(email: string): Promise<SendCodeResult> {
    return authApi.sendEmailCode({ email, purpose: 'register' })
  }

  /** 发送重置密码验证码（忘记密码），cooldown 同样用于重发倒计时 */
  async function sendResetCode(email: string): Promise<SendCodeResult> {
    return authApi.sendEmailCode({ email, purpose: 'reset' })
  }

  /**
   * 重置密码（忘记密码）。
   *
   * 服务端会让该账号此前签发的刷新令牌全部失效、并清掉 Cookie，本地这枚访问令牌
   * 也不该再用；这里顺手清空本地登录态，免得界面停在「已登录」而请求全 401。
   */
  async function resetPassword(payload: ResetPasswordRequest): Promise<void> {
    await authApi.resetPassword(payload)
    clearAccessToken()
    profile.value = null
  }

  /** 上传头像：成功后用后端返回的资料覆盖本地 profile，顶栏头像随之更新 */
  async function updateAvatar(file: File): Promise<void> {
    profile.value = await authApi.uploadAvatar(file)
  }

  /** 更新个人资料：成功后用后端返回的资料覆盖本地 profile，页面与顶栏随之刷新 */
  async function updateProfile(payload: UpdateProfileRequest): Promise<void> {
    profile.value = await authApi.updateProfile(payload)
  }

  /** 修改密码：成功后用后端返回的资料覆盖本地 profile；当前登录态不受影响 */
  async function changePassword(payload: ChangePasswordRequest): Promise<void> {
    profile.value = await authApi.changePassword(payload)
  }

  /** 退出登录：即使后端不可用也要清掉本地状态，避免用户卡住 */
  async function logout(): Promise<void> {
    try {
      await authApi.logout()
    } catch {
      // 忽略：本地登出优先
    }
    clearAccessToken()
    profile.value = null
  }

  // bootstrap 幂等：路由守卫在每次导航时调用，只有第一次真正发请求
  let bootstrapPromise: Promise<void> | null = null

  /**
   * 恢复登录态。
   *
   * 访问令牌只在内存中，页面刷新后会丢失；此时用 HttpOnly Cookie 里的
   * 刷新令牌换一个新的访问令牌，实现「刷新页面不掉登录」。
   * 未登录用户这里会静默失败，不产生任何提示。
   */
  function bootstrap(): Promise<void> {
    if (!bootstrapPromise) {
      bootstrapPromise = doBootstrap()
    }
    return bootstrapPromise
  }

  async function doBootstrap(): Promise<void> {
    try {
      const pair = await authApi.refresh()
      setAccessToken(pair.access_token)
      profile.value = await authApi.fetchProfile()
    } catch {
      clearAccessToken()
      profile.value = null
    }
  }

  return {
    profile,
    isLoggedIn,
    isAdmin,
    login,
    register,
    sendEmailCode,
    sendResetCode,
    resetPassword,
    updateAvatar,
    updateProfile,
    changePassword,
    logout,
    bootstrap,
  }
})
