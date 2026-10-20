// 管理端接口：用户管理（/admin/users）。
//
// 与 api/auth.ts 的分界：那边是「本人」的登录态与资料（/auth/*、/users/me），
// 这里是管理员对任意账号的操作，需要管理员身份。

import { http } from './request'
import type { AdminUpdateUserRequest, ApiEnvelope, PageResult, User } from '@/types/api'

/** 用户管理列表的查询条件；role / status 不传表示不过滤（0 也是合法取值） */
export interface AdminUserQuery {
  page?: number
  page_size?: number
  keyword?: string
  role?: number
  status?: number
}

/** 分页查询用户：GET /admin/users */
export async function listAdminUsers(query: AdminUserQuery): Promise<PageResult<User>> {
  const { data } = await http.get<ApiEnvelope<PageResult<User>>>('/admin/users', {
    params: query,
  })
  return data.data
}

/** 修改账号状态 / 角色：PATCH /admin/users/{id} */
export async function updateAdminUser(
  id: string,
  payload: AdminUpdateUserRequest,
): Promise<User> {
  const { data } = await http.patch<ApiEnvelope<User>>(`/admin/users/${id}`, payload)
  return data.data
}
