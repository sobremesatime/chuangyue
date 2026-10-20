// 举报相关接口，对应 OpenAPI 契约的 /reports 与 /admin/reports。

import { http } from './request'
import type {
  ApiEnvelope,
  CreateReportRequest,
  HandleReportRequest,
  PageResult,
  Report,
} from '@/types/api'

/** 发起举报（需登录），返回新建的举报记录 */
export async function createReport(payload: CreateReportRequest): Promise<Report> {
  const { data } = await http.post<ApiEnvelope<Report>>('/reports', payload)
  return data.data
}

export interface ListAdminReportParams {
  page?: number
  page_size?: number
  /** 0 待处理，1 已处理，2 已忽略；不传则返回全部 */
  status?: number
}

/** 管理后台举报列表（分页） */
export async function listAdminReports(
  params: ListAdminReportParams = {},
): Promise<PageResult<Report>> {
  const { data } = await http.get<ApiEnvelope<PageResult<Report>>>('/admin/reports', { params })
  return data.data
}

/** 处理举报：1 已处理、2 已忽略，返回更新后的记录 */
export async function handleReport(id: number | string, status: 1 | 2): Promise<Report> {
  const payload: HandleReportRequest = { status }
  const { data } = await http.patch<ApiEnvelope<Report>>(`/admin/reports/${id}`, payload)
  return data.data
}