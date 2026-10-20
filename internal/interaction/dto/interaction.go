// Package dto 定义互动域互动能力的请求与响应结构，字段与 OpenAPI 契约保持一致。
package dto

// InteractionState 是当前用户对某个视频的互动状态。
//
// 单独开接口查询而不是塞进视频详情：详情接口是公开的、不需要登录，
// 而互动状态依赖登录用户，混在一起会给公开接口引入可选鉴权。
//
// Progress 为上次观看进度（秒），无记录时为 0，供播放页做续播。
type InteractionState struct {
	Liked      bool `json:"liked"`
	Favorited  bool `json:"favorited"`
	WatchLater bool `json:"watch_later"`
	Progress   int  `json:"progress"`
}

// LikeResult 是点赞 / 取消点赞的结果。
//
// 顺带返回最新的点赞总数，前端按钮旁的计数无需再发一次详情请求。
type LikeResult struct {
	Liked     bool  `json:"liked"`
	LikeCount int64 `json:"like_count"`
}

// FavoriteResult 是收藏 / 取消收藏的结果。
//
// 与点赞一样顺带返回最新的收藏总数，前端按钮旁的计数无需再发一次详情请求。
type FavoriteResult struct {
	Favorited     bool  `json:"favorited"`
	FavoriteCount int64 `json:"favorite_count"`
}

// HistoryInput 是观看历史上报的请求体。
type HistoryInput struct {
	// Progress 为播放进度（秒），未传或为负时按 0 处理。
	Progress int `json:"progress"`
}

// WatchLaterResult 是加入 / 移出稍后再看的结果。
//
// 「稍后再看」是纯私人的待看清单，视频上没有对应计数列，
// 因此只回状态，不像收藏那样顺带返回总数。
type WatchLaterResult struct {
	WatchLater bool `json:"watch_later"`
}
