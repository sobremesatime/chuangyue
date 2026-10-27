// 合集 / 播放列表相关接口。
//
// 公开侧：观看页的「所属合集」、合集详情页；
// 管理侧：合集增删改、合集内视频的添加 / 移除 / 排序。
// 后端按约定返回 JSON snake_case、ID 为字符串，请求体字段也保持 snake_case。

import { http } from './request'
import type {
  AdminCollection,
  ApiEnvelope,
  CollectionDetail,
  CollectionListItem,
  CollectionPage,
  CreateCollectionPayload,
  UpdateCollectionPayload,
  VideoCollectionContext,
} from '@/types/api'

export interface ListCollectionParams {
  page?: number
  page_size?: number
}

/** 某个视频所属的合集（至多一个）与当前集号：GET /videos/{id}/collections */
export async function getVideoCollection(videoId: string): Promise<VideoCollectionContext> {
  const { data } = await http.get<ApiEnvelope<VideoCollectionContext>>(
    `/videos/${videoId}/collections`,
  )
  return data.data
}

/** 公开合集列表（分页）：GET /collections */
export async function listCollections(
  params: ListCollectionParams = {},
): Promise<CollectionPage<CollectionListItem>> {
  const { data } = await http.get<ApiEnvelope<CollectionPage<CollectionListItem>>>('/collections', {
    params,
  })
  return data.data
}

/** 合集详情（含全部视频，按合集内顺序）：GET /collections/{id} */
export async function getCollection(id: string): Promise<CollectionDetail> {
  const { data } = await http.get<ApiEnvelope<CollectionDetail>>(`/collections/${id}`)
  return data.data
}

/**
 * 管理端合集详情（含草稿合集与未上架视频）：GET /admin/collections/{id}
 *
 * 编排面板不能用公开详情：公开读会过滤草稿与未上架视频，
 * 拿到的顺序与库内不一致，重排会被服务端判为「集合不一致」。
 */
export async function getAdminCollection(id: string): Promise<CollectionDetail> {
  const { data } = await http.get<ApiEnvelope<CollectionDetail>>(`/admin/collections/${id}`)
  return data.data
}

/** 管理端合集列表（分页，含草稿）：GET /admin/collections */
export async function listAdminCollections(
  params: ListCollectionParams = {},
): Promise<CollectionPage<AdminCollection>> {
  const { data } = await http.get<ApiEnvelope<CollectionPage<AdminCollection>>>(
    '/admin/collections',
    { params },
  )
  return data.data
}

/** 新建合集：POST /admin/collections */
export async function createCollection(
  payload: CreateCollectionPayload,
): Promise<AdminCollection> {
  const { data } = await http.post<ApiEnvelope<AdminCollection>>('/admin/collections', payload)
  return data.data
}

/** 更新合集（标题 / 简介 / 封面 / 状态）：PUT /admin/collections/{id} */
export async function updateCollection(
  id: string,
  payload: UpdateCollectionPayload,
): Promise<AdminCollection> {
  const { data } = await http.put<ApiEnvelope<AdminCollection>>(`/admin/collections/${id}`, payload)
  return data.data
}

/** 删除合集（不会删除其中的视频）：DELETE /admin/collections/{id} */
export async function deleteCollection(id: string): Promise<void> {
  await http.delete(`/admin/collections/${id}`)
}

/**
 * 向合集追加视频：POST /admin/collections/{id}/videos
 *
 * 按数组顺序追加到合集末尾，不改变已有视频的次序。
 */
export async function addCollectionVideos(collectionId: string, videoIds: string[]): Promise<void> {
  await http.post(`/admin/collections/${collectionId}/videos`, { video_ids: videoIds })
}

/** 从合集移除单个视频：DELETE /admin/collections/{id}/videos/{videoId} */
export async function removeCollectionVideo(collectionId: string, videoId: string): Promise<void> {
  await http.delete(`/admin/collections/${collectionId}/videos/${videoId}`)
}

/** 重排合集内视频：PATCH /admin/collections/{id}/order，video_ids 即最终顺序 */
export async function reorderCollectionVideos(
  collectionId: string,
  videoIds: string[],
): Promise<void> {
  await http.patch(`/admin/collections/${collectionId}/order`, { video_ids: videoIds })
}
