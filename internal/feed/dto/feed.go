// Package dto 定义 feed 服务缓存所用的数据结构。
//
// 接口出参不在这里：推荐流返回的卡片形状由视频域定义（videodto.VideoBrief），
// feed 只是使用者，避免各消费域各写一份相同字段的结构。
package dto

import (
	"time"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// VideoBrief 是首页流里单个视频的落缓存形态，字段与前端展示所需对齐。
//
// 为什么不直接把 protobuf 生成的 *videov1.VideoBrief 做 JSON 缓存：生成的结构体
// 带有 state / sizeCache / unknownFields 等内部状态字段，encoding/json 会把它们一并
// 序列化，产物既脏又可能因内部字段不可序列化而失败。因此用这份干净 DTO 转换后再缓存。
type VideoBrief struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	UploaderID int64     `json:"uploader_id"`
	Status     int32     `json:"status"`
	CoverURL   string    `json:"cover_url"`
	CategoryID int64     `json:"category_id"`
	Duration   int32     `json:"duration"`
	ViewCount  int64     `json:"view_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// FromProto 把 proto 的 VideoBrief 转成缓存 DTO。
func FromProto(v *videov1.VideoBrief) *VideoBrief {
	out := &VideoBrief{
		ID:         v.GetId(),
		Title:      v.GetTitle(),
		UploaderID: v.GetUploaderId(),
		Status:     v.GetStatus(),
		CoverURL:   v.GetCoverUrl(),
		CategoryID: v.GetCategoryId(),
		Duration:   v.GetDuration(),
		ViewCount:  v.GetViewCount(),
	}
	// 时间戳缺省时保持零值：AsTime 对 nil 会返回 Unix 零点，直接写进去会落成 1970。
	if ts := v.GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime()
	}
	return out
}

// ToProto 把缓存 DTO 还原成 proto 的 VideoBrief。
func (d *VideoBrief) ToProto() *videov1.VideoBrief {
	out := &videov1.VideoBrief{
		Id:         d.ID,
		Title:      d.Title,
		UploaderId: d.UploaderID,
		Status:     d.Status,
		CoverUrl:   d.CoverURL,
		CategoryId: d.CategoryID,
		Duration:   d.Duration,
		ViewCount:  d.ViewCount,
	}
	if !d.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(d.CreatedAt)
	}
	return out
}
