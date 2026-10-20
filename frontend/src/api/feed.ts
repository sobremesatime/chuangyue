// 首页推荐流接口，对应网关的 GET /feed。
//
// 与视频列表（/videos）的分工：/feed 只做推荐——未登录按全局热度、登录按个性化，
// 不支持 category_id / keyword 过滤；分类筛选与搜索仍走 /videos。

import { http } from './request'
import type { ApiEnvelope, PageResult, VideoCardItem } from '@/types/api'

export interface HomeFeedParams {
  page?: number
  page_size?: number
}

/** 首页推荐流（分页，返回与视频列表同口径的精简卡片） */
export async function getHomeFeed(params: HomeFeedParams = {}): Promise<PageResult<VideoCardItem>> {
  const { data } = await http.get<ApiEnvelope<PageResult<VideoCardItem>>>('/feed', { params })
  return data.data
}
