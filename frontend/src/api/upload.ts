// 分片上传相关接口，对应 OpenAPI 契约的 /admin/uploads*。
//
// 管理员上传大文件的流程：
//   1. initUpload     用整文件 MD5 初始化会话；命中秒传直接返回 video_id，
//                     命中「上传中」的会话则返回已上传分片，供断点续传；
//   2. putPart        逐片直传原始二进制（application/octet-stream），可并发；
//   3. completeUpload 分片齐全后合并为源文件并登记视频（状态「转码中」）；
//   4. abortUpload    中止会话并释放已上传分片。
//
// 分片直传地址（presign）当前本地磁盘驱动不支持（业务错误码 30013），
// 因此这里不封装，前端统一走网关的 putPart。

import { http } from './request'
import type { ApiEnvelope, Video } from '@/types/api'
import type { components } from '@/types/openapi'

type Schemas = components['schemas']

/** 初始化上传请求体 */
export type InitUploadRequest = Schemas['InitUploadRequest']

/** 单个已上传分片，part_number 从 1 开始 */
export type UploadedPart = Schemas['UploadedPart']

/** 初始化 / 查询会话的返回值：instant 时带 video_id，multipart 时带分片计划与已上传分片 */
export type UploadSession = Schemas['InitUploadResponse']

/** 完成上传请求体（省略 parts 时以服务端已记录的分片为准） */
export type CompleteUploadRequest = Schemas['CompleteUploadRequest']

/** 初始化分片上传（或命中秒传 / 命中可续传会话） */
export async function initUpload(payload: InitUploadRequest): Promise<UploadSession> {
  const { data } = await http.post<ApiEnvelope<UploadSession>>('/admin/uploads', payload)
  return data.data
}

/** 查询上传会话进度（断点续传用，返回分片计划与已上传分片） */
export async function getUploadStatus(uploadId: string): Promise<UploadSession> {
  const { data } = await http.get<ApiEnvelope<UploadSession>>(`/admin/uploads/${uploadId}`)
  return data.data
}

/**
 * 上传单个分片。
 *
 * 请求体是该分片的原始二进制，必须显式声明 application/octet-stream，
 * 否则 axios 会按 Blob 的 type（mkv 等格式可能为空）发送；
 * 大分片走默认 15s 超时会中途断开，这里同样关掉超时。
 */
export async function putPart(
  uploadId: string,
  partNumber: number,
  blob: Blob,
): Promise<UploadedPart> {
  const { data } = await http.put<ApiEnvelope<UploadedPart>>(
    `/admin/uploads/${uploadId}/parts/${partNumber}`,
    blob,
    {
      headers: { 'Content-Type': 'application/octet-stream' },
      timeout: 0,
    },
  )
  return data.data
}

/** 完成上传：服务端校验分片齐全后合并并登记视频，返回转码中的视频 */
export async function completeUpload(
  uploadId: string,
  payload: CompleteUploadRequest = {},
): Promise<Video> {
  const { data } = await http.post<ApiEnvelope<Video>>(
    `/admin/uploads/${uploadId}/complete`,
    payload,
  )
  return data.data
}

/** 中止上传：释放该会话已上传的分片 */
export async function abortUpload(uploadId: string): Promise<void> {
  await http.delete(`/admin/uploads/${uploadId}`)
}