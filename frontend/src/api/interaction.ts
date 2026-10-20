// 用户与视频的互动接口：点赞、收藏、稍后再看、观看历史。
// 对应 OpenAPI 契约的 /videos/{id}/* 与 /users/me/*。

import { http } from './request'
import type {
  ApiEnvelope,
  FavoriteResult,
  InteractionState,
  LikeResult,
  PageResult,
  Video,
  WatchLaterResult,
} from '@/types/api'

/** 查询当前用户对该视频的点赞 / 收藏状态 */
export async function getInteractionState(id: number | string): Promise<InteractionState> {
  const { data } = await http.get<ApiEnvelope<InteractionState>>(`/videos/${id}/interaction`)
  return data.data
}

/** 点赞（liked=true）或取消点赞（liked=false），返回最新点赞总数 */
export async function setLike(id: number | string, liked: boolean): Promise<LikeResult> {
  const url = `/videos/${id}/like`
  const { data } = liked
    ? await http.post<ApiEnvelope<LikeResult>>(url)
    : await http.delete<ApiEnvelope<LikeResult>>(url)
  return data.data
}

/** 收藏（favorited=true）或取消收藏（favorited=false） */
export async function setFavorite(
  id: number | string,
  favorited: boolean,
): Promise<FavoriteResult> {
  const url = `/videos/${id}/favorite`
  const { data } = favorited
    ? await http.post<ApiEnvelope<FavoriteResult>>(url)
    : await http.delete<ApiEnvelope<FavoriteResult>>(url)
  return data.data
}

/**
 * 加入（watchLater=true）或移出（watchLater=false）稍后再看。
 *
 * 与收藏相互独立：收藏表达「喜欢」，稍后再看表达「待看」。
 */
export async function setWatchLater(
  id: number | string,
  watchLater: boolean,
): Promise<WatchLaterResult> {
  const url = `/videos/${id}/watch-later`
  const { data } = watchLater
    ? await http.post<ApiEnvelope<WatchLaterResult>>(url)
    : await http.delete<ApiEnvelope<WatchLaterResult>>(url)
  return data.data
}

/**
 * 上报观看历史。
 *
 * 进度为秒；后端按 (user, video) 唯一，重复上报只更新进度与时间，
 * 因此进入页面报一次 0、离开时再报当前进度即可。
 */
export async function reportHistory(id: number | string, progress: number): Promise<void> {
  const seconds = Number.isFinite(progress) ? Math.max(0, Math.floor(progress)) : 0
  await http.post(`/videos/${id}/history`, { progress: seconds })
}

/** 「我」的列表：赞过的视频 / 我的收藏 / 稍后再看 / 观看历史 */
export type MyVideoKind = 'likes' | 'favorites' | 'watch-later' | 'history'

export interface ListMyVideosParams {
  page?: number
  page_size?: number
}

/** 分页查询「我」列表，后端只返回仍上架的视频 */
export async function listMyVideos(
  kind: MyVideoKind,
  params: ListMyVideosParams = {},
): Promise<PageResult<Video>> {
  const { data } = await http.get<ApiEnvelope<PageResult<Video>>>(`/users/me/${kind}`, { params })
  return data.data
}
