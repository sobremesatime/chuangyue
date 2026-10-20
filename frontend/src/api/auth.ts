// 认证相关接口，对应 OpenAPI 契约的 /auth/* 与 /users/me

import { http } from './request'
import type {
  ApiEnvelope,
  ChangePasswordRequest,
  RegisterRequest,
  ResetPasswordRequest,
  SendCodeRequest,
  SendCodeResult,
  TokenPair,
  UpdateProfileRequest,
  User,
} from '@/types/api'

export interface LoginPayload {
  /** 用户名或邮箱：含 @ 时后端按邮箱查询 */
  account: string
  password: string
}

/** 登录：后端返回访问令牌，并把刷新令牌写入 HttpOnly Cookie */
export async function login(payload: LoginPayload): Promise<TokenPair> {
  const { data } = await http.post<ApiEnvelope<TokenPair>>('/auth/login', payload)
  return data.data
}

/** 注册：后端只创建普通用户，管理员需另行初始化 */
export async function register(payload: RegisterRequest): Promise<User> {
  const { data } = await http.post<ApiEnvelope<User>>('/auth/register', payload)
  return data.data
}

/** 发送邮箱验证码；purpose 区分用途（register 注册 / reset 重置密码） */
export async function sendEmailCode(payload: SendCodeRequest): Promise<SendCodeResult> {
  const { data } = await http.post<ApiEnvelope<SendCodeResult>>('/auth/email/code', payload)
  return data.data
}

/**
 * 用邮箱验证码重置密码（忘记密码，匿名可调）。
 *
 * 成功后后端会让该账号此前签发的刷新令牌全部失效并清掉 Cookie，
 * 因此调用方应把本地登录态一并清空，引导用户用新密码重新登录。
 */
export async function resetPassword(payload: ResetPasswordRequest): Promise<void> {
  await http.post<ApiEnvelope<null>>('/auth/password/reset', payload)
}

/** 刷新访问令牌（依赖 Cookie 中的刷新令牌） */
export async function refresh(): Promise<TokenPair> {
  const { data } = await http.post<ApiEnvelope<TokenPair>>('/auth/refresh')
  return data.data
}

/** 退出登录：后端清除刷新令牌 Cookie */
export async function logout(): Promise<void> {
  await http.post<ApiEnvelope<null>>('/auth/logout')
}

/** 获取当前登录用户资料 */
export async function fetchProfile(): Promise<User> {
  const { data } = await http.get<ApiEnvelope<User>>('/users/me')
  return data.data
}

/** 上传 / 更换当前用户头像（multipart/form-data），返回更新后的用户资料 */
export async function uploadAvatar(file: File): Promise<User> {
  const form = new FormData()
  form.append('file', file)
  const { data } = await http.post<ApiEnvelope<User>>('/users/me/avatar', form)
  return data.data
}

/** 更新当前登录用户资料（部分更新），返回更新后的用户资料 */
export async function updateProfile(payload: UpdateProfileRequest): Promise<User> {
  const { data } = await http.patch<ApiEnvelope<User>>('/users/me', payload)
  return data.data
}

/** 修改当前用户密码：需要携带当前密码，返回更新后的用户资料 */
export async function changePassword(payload: ChangePasswordRequest): Promise<User> {
  const { data } = await http.put<ApiEnvelope<User>>('/users/me/password', payload)
  return data.data
}
