// 视频评论接口：列表、发表、删除、点赞。
// 对应 OpenAPI 契约的 /videos/{id}/comments 与 /comments/{id}。

import { http } from './request'
import type { AdminComment, ApiEnvelope, Comment, CommentLikeResult, PageResult } from '@/types/api'

export interface ListCommentParams {
  page?: number
  page_size?: number
}

/** 视频评论列表（分页，按发表时间倒序） */
export async function listComments(
  videoId: number | string,
  params: ListCommentParams = {},
): Promise<PageResult<Comment>> {
  const { data } = await http.get<ApiEnvelope<PageResult<Comment>>>(
    `/videos/${videoId}/comments`,
    { params },
  )
  return data.data
}

/** 发表评论，返回新建的评论（含作者信息） */
export async function createComment(
  videoId: number | string,
  content: string,
  parentId?: number | string,
): Promise<Comment> {
  const { data } = await http.post<ApiEnvelope<Comment>>(`/videos/${videoId}/comments`, {
    content,
    ...(parentId ? { parent_id: String(parentId) } : {}),
  })
  return data.data
}

/** 某条一级评论下的回复列表（分页，按发表时间正序） */
export async function listReplies(
  commentId: number | string,
  params: ListCommentParams = {},
): Promise<PageResult<Comment>> {
  const { data } = await http.get<ApiEnvelope<PageResult<Comment>>>(
    `/comments/${commentId}/replies`,
    { params },
  )
  return data.data
}

/** 删除评论（仅作者本人或管理员） */
export async function deleteComment(commentId: number | string): Promise<void> {
  await http.delete(`/comments/${commentId}`)
}

/** 点赞（liked=true）或取消点赞（liked=false）评论，返回最新点赞总数 */
export async function setCommentLike(
  commentId: number | string,
  liked: boolean,
): Promise<CommentLikeResult> {
  const url = `/comments/${commentId}/like`
  const { data } = liked
    ? await http.post<ApiEnvelope<CommentLikeResult>>(url)
    : await http.delete<ApiEnvelope<CommentLikeResult>>(url)
  return data.data
}

export interface ListAdminCommentParams {
  page?: number
  page_size?: number
  /** 按评论内容模糊匹配 */
  keyword?: string
}

/** 管理后台评论列表（分页，含所属视频与作者信息） */
export async function listAdminComments(
  params: ListAdminCommentParams = {},
): Promise<PageResult<AdminComment>> {
  const { data } = await http.get<ApiEnvelope<PageResult<AdminComment>>>('/admin/comments', {
    params,
  })
  return data.data
}

/** 删除评论（软删，管理端） */
export async function deleteAdminComment(id: number | string): Promise<void> {
  await http.delete(`/admin/comments/${id}`)
}
