// 站内通知接口：列表、未读数、标记已读。
// 对应 OpenAPI 契约的 /users/me/notifications*。

import { http } from './request'
import type { ApiEnvelope, AppNotification, NotificationUnread, PageResult } from '@/types/api'

export interface ListNotificationParams {
  page?: number
  page_size?: number
}

/** 我的通知列表（分页，按时间倒序） */
export async function listNotifications(
  params: ListNotificationParams = {},
): Promise<PageResult<AppNotification>> {
  const { data } = await http.get<ApiEnvelope<PageResult<AppNotification>>>(
    '/users/me/notifications',
    { params },
  )
  return data.data
}

/** 查询未读通知数，供顶栏红点轮询 */
export async function fetchUnreadCount(): Promise<NotificationUnread> {
  const { data } = await http.get<ApiEnvelope<NotificationUnread>>(
    '/users/me/notifications/unread',
  )
  return data.data
}

/** 标记已读：传 id 标记单条，不传表示全部已读，返回最新的未读数 */
export async function markNotificationsRead(id?: number | string): Promise<NotificationUnread> {
  const body = id ? { id: String(id) } : {}
  const { data } = await http.post<ApiEnvelope<NotificationUnread>>(
    '/users/me/notifications/read',
    body,
  )
  return data.data
}
