// 前端类型定义。
//
// 核心实体（User / Video / TokenPair 等）不手写，统一从 OpenAPI 契约生成：
//   npm run gen:types
// 生成结果位于 src/types/openapi.d.ts，后端契约变更后重新生成即可。
//
// 这里只补充契约里没有描述的部分：统一信封、分页结构、业务常量。

import type { components } from './openapi'

type Schemas = components['schemas']

/** 用户信息（字段来自契约，Required 去掉「全部可选」带来的噪音） */
export type User = Required<Schemas['User']>

/** 视频信息 */
export type Video = Required<Schemas['Video']>

/**
 * 视频卡片渲染所需的最小字段集。
 *
 * 首页推荐流（GET /feed）返回的是精简卡片，只含这些字段；视频列表（GET /videos）返回完整
 * Video、是它的超集。VideoCard 按最小需求声明入参，于是两种来源都能直接传入。
 */
export type VideoCardItem = Pick<
  Video,
  'id' | 'title' | 'cover_url' | 'duration' | 'view_count' | 'created_at'
>

/** 访问令牌信息（刷新令牌在 HttpOnly Cookie 中，不在这里） */
export type TokenPair = Required<Schemas['TokenPair']>

/** 注册请求体 */
export type RegisterRequest = Schemas['RegisterRequest']

/** 登录请求体 */
export type LoginRequest = Schemas['LoginRequest']

/** 发送邮箱验证码请求体 */
export type SendCodeRequest = Schemas['SendCodeRequest']

/** 发送邮箱验证码结果，用于前端重发倒计时 */
export type SendCodeResult = Required<Schemas['SendCodeResponse']>

/** 更新个人资料请求体（字段均可选，只传需要改的） */
export type UpdateProfileRequest = Schemas['UpdateProfileRequest']

/** 修改密码请求体（当前密码 + 新密码） */
export type ChangePasswordRequest = Schemas['ChangePasswordRequest']

/** 重置密码请求体（忘记密码通道：邮箱 + 一次性验证码 + 新密码，无需登录） */
export type ResetPasswordRequest = Schemas['ResetPasswordRequest']

/** 管理端修改账号状态 / 角色（两个字段都可选，但至少给一个） */
export type AdminUpdateUserRequest = Schemas['AdminUpdateUserRequest']

/** 视频互动状态：当前用户是否已点赞 / 已收藏，以及上次观看进度 */
export type InteractionState = Required<Schemas['InteractionState']>

/**
 * 视频评论。
 * replies 是一级评论内嵌的回复预览，这里显式改成递归的 Comment[]：
 * 契约生成的自引用类型不会带上 Required，嵌套层级会退化成「字段都可选」，与上层不一致。
 */
export type Comment = Required<Omit<Schemas['Comment'], 'replies'>> & { replies: Comment[] }

/** 发表评论请求体 */
export type CreateCommentRequest = Schemas['CreateCommentRequest']

/** 评论点赞 / 取消点赞结果（含最新点赞总数） */
export type CommentLikeResult = Required<Schemas['CommentLikeResult']>

/** 视频弹幕 */
export type Danmaku = Required<Schemas['Danmaku']>

/** 发表弹幕请求体 */
export type CreateDanmakuRequest = Schemas['CreateDanmakuRequest']

/** 举报记录（管理端处理用） */
export type Report = Required<Schemas['Report']>

/** 管理端评论视图（含所属视频与作者信息） */
export type AdminComment = Required<Schemas['AdminComment']>

/** 管理端弹幕视图（含所属视频与发送者信息） */
export type AdminDanmaku = Required<Schemas['AdminDanmaku']>

/** 发起举报请求体 */
export type CreateReportRequest = Schemas['CreateReportRequest']

/** 处理举报请求体（1 已处理，2 已忽略） */
export type HandleReportRequest = Schemas['HandleReportRequest']

/** 点赞 / 取消点赞结果（含最新点赞总数） */
export type LikeResult = Required<Schemas['LikeResult']>

/** 收藏 / 取消收藏结果 */
export type FavoriteResult = Required<Schemas['FavoriteResult']>

/** 加入 / 移出稍后再看结果 */
export type WatchLaterResult = Required<Schemas['WatchLaterResult']>

/** 观看历史上报请求体 */
export type HistoryRequest = Schemas['HistoryRequest']

/**
 * 站内通知。
 * 命名为 AppNotification，避免与浏览器内置的 DOM Notification 同名造成困扰。
 */
export type AppNotification = Required<Schemas['Notification']>

/** 未读通知数（供顶栏红点轮询） */
export type NotificationUnread = Required<Schemas['NotificationUnread']>

/** 标记通知已读请求体（id 缺省或为 0 表示全部已读） */
export type MarkNotificationsReadRequest = Schemas['MarkNotificationsReadRequest']

/**
 * 统一响应信封。
 * 后端所有接口都返回该结构，code 为 0 表示成功。
 */
export interface ApiEnvelope<T = unknown> {
  code: number
  message: string
  data: T
  trace_id?: string
}

/** 分页结果 */
export interface PageResult<T> {
  list: T[]
  total: number
  page: number
  page_size: number
}

/**
 * 合集分页结果。
 *
 * 契约里合集的列表字段叫 items，与通用 PageResult 的 list 不同；
 * 这里单独声明，避免在页面里做「result.items ?? result.list」这类兼容分支。
 */
export interface CollectionPage<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

/** 角色常量，与后端 internal/user/model 保持一致 */
export const ROLE_USER = 0
export const ROLE_ADMIN = 1

/** 账号状态常量，与后端 internal/user/model 保持一致 */
export const USER_STATUS_DISABLED = 0
export const USER_STATUS_NORMAL = 1

/** 视频状态常量，与后端 internal/video/model 保持一致 */
export const VIDEO_STATUS_TRANSCODING = 0
export const VIDEO_STATUS_PUBLISHED = 1
export const VIDEO_STATUS_OFFLINE = 2
export const VIDEO_STATUS_FAILED = 3

/**
 * 合集相关类型。
 *
 * 集合由后端独立实现，尚未写进 OpenAPI 契约文件，这里先手写；
 * 字段严格按前后端约定（JSON snake_case、ID 为字符串），契约更新后可改为生成。
 */

/** 某个视频所属的合集：观看页右侧「合集」卡片用，current_index 为当前视频在合集内的序号 */
export interface VideoCollectionRef {
  id: string
  title: string
  cover_url: string
  video_count: number
  current_index: number
}

/** 公开合集列表项 */
export interface CollectionListItem {
  id: string
  title: string
  cover_url: string
  video_count: number
  created_at: string
}

/** 合集详情：videos 的字段与视频详情接口一致 */
export interface CollectionDetail extends CollectionListItem {
  description: string
  videos: Video[]
}

/** 管理端合集：比公开列表多出简介与状态（含草稿） */
export interface AdminCollection extends CollectionListItem {
  description: string
  status: number
}

/** 新建合集请求体 */
export interface CreateCollectionPayload {
  title: string
  description?: string
  cover_url?: string
}

/** 更新合集请求体：字段均可选，只传需要改的 */
export interface UpdateCollectionPayload {
  title?: string
  description?: string
  cover_url?: string
  status?: number
}

/** 合集状态常量：0 草稿，1 已发布 */
export const COLLECTION_STATUS_DRAFT = 0
export const COLLECTION_STATUS_PUBLISHED = 1

/** 举报对象类型，与后端 internal/report/model 保持一致 */
export const REPORT_TARGET_VIDEO = 1
export const REPORT_TARGET_COMMENT = 2

/** 举报处理状态，与后端 internal/report/model 保持一致 */
export const REPORT_STATUS_PENDING = 0
export const REPORT_STATUS_HANDLED = 1
export const REPORT_STATUS_IGNORED = 2

/**
 * 举报理由白名单，与后端校验一致，顺序即界面展示顺序。
 * 前端只提供候选项，合法性最终由后端把关。
 */
export const REPORT_REASONS: string[] = [
  '垃圾广告',
  '色情低俗',
  '违法违规',
  '侵权盗用',
  '引战辱骂',
  '其他',
]

/** 视频分类 */
export interface VideoCategory {
  id: number
  name: string
}

/** 「全部」筛选项：查询时 category_id 为 0 表示不按分类过滤 */
export const ALL_CATEGORY_ID = 0

/**
 * 分类清单。
 *
 * 后端目前只有 videos.category_id 这个数字字段，分类管理接口在后续版本提供；
 * 在那之前名称由前端维护，与上传页的分类下拉共用同一份定义。
 * 数组顺序即界面展示顺序，「全部」由筛选组件补在最前面。
 */
export const VIDEO_CATEGORIES: VideoCategory[] = [
  { id: 1, name: '电影' },
  { id: 2, name: '剧集' },
  { id: 3, name: '动漫' },
  { id: 4, name: '音乐' },
  { id: 5, name: '游戏' },
]
