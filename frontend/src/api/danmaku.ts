// 视频弹幕接口：拉取、发表、删除。
// 对应 OpenAPI 契约的 /videos/{id}/danmaku 与 /danmaku/{id}。

import { http } from './request'
import type { AdminDanmaku, ApiEnvelope, CreateDanmakuRequest, Danmaku, PageResult } from '@/types/api'

/** 拉取某视频的全部弹幕（按时间轴升序） */
export async function listDanmaku(videoId: number | string): Promise<Danmaku[]> {
  const { data } = await http.get<ApiEnvelope<Danmaku[]>>(`/videos/${videoId}/danmaku`)
  return data.data ?? []
}

/** 发表弹幕，返回新建的弹幕（含发送者信息） */
export async function createDanmaku(
  videoId: number | string,
  payload: CreateDanmakuRequest,
): Promise<Danmaku> {
  const { data } = await http.post<ApiEnvelope<Danmaku>>(`/videos/${videoId}/danmaku`, payload)
  return data.data
}

/** 删除弹幕（仅发送者本人或管理员） */
export async function deleteDanmaku(danmakuId: number | string): Promise<void> {
  await http.delete(`/danmaku/${danmakuId}`)
}

export interface ListAdminDanmakuParams {
  page?: number
  page_size?: number
  /** 按弹幕内容模糊匹配 */
  keyword?: string
}

/** 管理后台弹幕列表（分页，含所属视频与发送者信息） */
export async function listAdminDanmaku(
  params: ListAdminDanmakuParams = {},
): Promise<PageResult<AdminDanmaku>> {
  const { data } = await http.get<ApiEnvelope<PageResult<AdminDanmaku>>>('/admin/danmaku', {
    params,
  })
  return data.data
}

/** 删除弹幕（软删，管理端） */
export async function deleteAdminDanmaku(id: number | string): Promise<void> {
  await http.delete(`/admin/danmaku/${id}`)
}
