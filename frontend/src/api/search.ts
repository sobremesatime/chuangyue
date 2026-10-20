// 视频检索接口，对应网关的 GET /search/videos。
//
// 与视频列表（/videos）的分工：/search/videos 走独立搜索服务，支持「关键词 + 分类」组合；
// /videos 承担分类浏览（只选分类、无关键词）与视频列表展示。

import { http } from './request'
import type { ApiEnvelope, PageResult, VideoCardItem } from '@/types/api'

export interface SearchVideosParams {
  /** 关键词，为空表示不按关键词过滤 */
  keyword?: string
  /** 分类 ID，0 或不传表示不按分类过滤 */
  category_id?: number
  page?: number
  page_size?: number
}

/** 视频检索（分页，返回与视频列表同口径的精简卡片） */
export async function searchVideos(params: SearchVideosParams = {}): Promise<PageResult<VideoCardItem>> {
  const { data } = await http.get<ApiEnvelope<PageResult<VideoCardItem>>>('/search/videos', { params })
  return data.data
}
