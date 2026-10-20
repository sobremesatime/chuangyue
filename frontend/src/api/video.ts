// 视频相关接口，对应 OpenAPI 契约的 /videos* 与 /admin/videos

import { http } from './request'
import type { ApiEnvelope, PageResult, Video } from '@/types/api'

export interface ListVideoParams {
  page?: number
  page_size?: number
  category_id?: number
  /** 搜索关键词，按标题或简介模糊匹配 */
  keyword?: string
  /** 排序方式：hot 按热度，new 或不传按最新发布 */
  sort?: 'new' | 'hot'
}

/** 视频列表（分页，仅返回已上架视频） */
export async function listVideos(params: ListVideoParams = {}): Promise<PageResult<Video>> {
  const { data } = await http.get<ApiEnvelope<PageResult<Video>>>('/videos', { params })
  return data.data
}

/** 视频详情 */
export async function getVideo(id: number | string): Promise<Video> {
  const { data } = await http.get<ApiEnvelope<Video>>(`/videos/${id}`)
  return data.data
}

export interface ListAdminVideoParams {
  page?: number
  page_size?: number
  /** 0 转码中，1 已上架，2 已下架；不传则返回全部状态 */
  status?: number
  /** 对标题 / 简介做子串匹配；不传表示不过滤 */
  keyword?: string
}

/** 管理后台视频列表（分页，含未上架视频） */
export async function listAdminVideos(
  params: ListAdminVideoParams = {},
): Promise<PageResult<Video>> {
  const { data } = await http.get<ApiEnvelope<PageResult<Video>>>('/admin/videos', { params })
  return data.data
}

/** 上架 / 下架视频：1 上架，2 下架 */
export async function updateVideoStatus(id: number | string, status: number): Promise<Video> {
  const { data } = await http.patch<ApiEnvelope<Video>>(`/admin/videos/${id}`, { status })
  return data.data
}

/**
 * 重新转码：只对「转码失败」的视频有效。
 *
 * 复用上传时已落盘的源文件，不重新传字节；成功后状态回到「转码中」。
 */
export async function retryTranscode(id: number | string): Promise<Video> {
  const { data } = await http.post<ApiEnvelope<Video>>(`/admin/videos/${id}/transcode`)
  return data.data
}

export interface UpdateVideoPayload {
  title: string
  description: string
  category_id: number
  /** 传了才替换封面，不传保持原封面 */
  cover?: File
}

/**
 * 编辑视频信息（multipart/form-data）。
 *
 * 后端以「字段是否存在」判断改不改，所以这里始终提交标题、简介、分类
 * ——简介为空串就是「清空简介」；封面只在选了新图时才放进请求。
 */
export async function updateVideo(
  id: number | string,
  payload: UpdateVideoPayload,
): Promise<Video> {
  const form = new FormData()
  form.append('title', payload.title)
  form.append('description', payload.description)
  form.append('category_id', String(payload.category_id))
  if (payload.cover) {
    form.append('cover', payload.cover)
  }

  const { data } = await http.put<ApiEnvelope<Video>>(`/admin/videos/${id}`, form)
  return data.data
}

/** 删除视频（不可恢复，后端会一并清理磁盘上的视频与封面文件） */
export async function deleteVideo(id: number | string): Promise<void> {
  await http.delete(`/admin/videos/${id}`)
}

export interface UploadVideoPayload {
  title: string
  description?: string
  category_id?: number
  file: File
  cover?: File
}

/**
 * 管理员上传视频（multipart/form-data）。
 *
 * 这里手写 FormData 而不是用 JSON，因为契约里该接口是 multipart；
 * 同时通过 onProgress 回调把上传进度交给页面展示。
 */
export async function uploadVideo(
  payload: UploadVideoPayload,
  onProgress?: (percent: number) => void,
): Promise<Video> {
  const form = new FormData()
  form.append('file', payload.file)
  form.append('title', payload.title)
  if (payload.description) {
    form.append('description', payload.description)
  }
  if (payload.category_id) {
    form.append('category_id', String(payload.category_id))
  }
  if (payload.cover) {
    form.append('cover', payload.cover)
  }

  const { data } = await http.post<ApiEnvelope<Video>>('/admin/videos', form, {
    // 视频动辄几百 MB，默认 15s 超时会在传完之前就断掉；这里显式关掉超时
    timeout: 0,
    onUploadProgress: (event) => {
      if (onProgress && event.total) {
        onProgress(Math.round((event.loaded / event.total) * 100))
      }
    },
  })
  return data.data
}
