package service

import (
	"context"

	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/internal/interaction/repository"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// UserBrief 是互动域对外部用户信息的最小需求。
//
// 只保留展示所需的字段，与 user.v1.UserBrief 契约字段一致，
// 这样互动域无需依赖用户域的模型即可装配昵称与头像。
type UserBrief struct {
	ID       int64
	Username string
	Nickname string
	Avatar   string
}

// UserProvider 提供用户展示信息，屏蔽「进程内直查」与「跨服务 gRPC」两种来源。
//
// 当前版本网关与互动服务同进程时，由装配层注入一个基于用户服务本地查询的适配器
// （见 internal/gateway/router/userprovider.go）；后续版本互动服务独立部署后，
// 改由 cmd/interaction 注入基于 user.v1.UserService 的 gRPC 适配器。
// 互动域只依赖本接口，不再 JOIN `user` 表，也不感知用户域的实现。
type UserProvider interface {
	// GetUsers 批量取用户信息；不存在的 id 不返回。返回 map 便于按 id 组装。
	GetUsers(ctx context.Context, ids []int64) (map[int64]UserBrief, error)
}

// loadUsers 调一次 UserProvider 取回这批 id 的用户信息。
//
// best-effort：提供者出错时只记警告并返回空表，让列表以「昵称头像留空」的方式降级，
// 不因为一个辅助依赖的抖动把评论 / 通知列表整体打挂（改造前的 JOIN 也不会让列表失败）。
func loadUsers(ctx context.Context, provider UserProvider, ids []int64) map[int64]UserBrief {
	if provider == nil || len(ids) == 0 {
		return nil
	}
	users, err := provider.GetUsers(ctx, ids)
	if err != nil {
		logger.L().Warn("批量获取用户信息失败，相关昵称头像留空", logger.TraceField(ctx), zap.Error(err))
		return nil
	}
	return users
}

// fillCommentUsers 批量补全评论 / 回复行的作者与被回复者展示信息。
//
// 这是一处与改造前 JOIN 的细微差异点：原 `JOIN user AS u`（内连接）在作者用户行缺失时
// 会丢掉整行；这里选择保留该行、仅把昵称头像留空。原因是分页与排序必须留在 SQL 层，
// 若在内存里丢行会打乱页内条数与 total（改造前丢行发生在 SQL 层、发生在分页之前）；
// 而实际业务中用户不会被物理删除，作者行缺失极罕见，留空比丢行更接近 LEFT JOIN 的容忍行为。
func fillCommentUsers(ctx context.Context, provider UserProvider, rows []repository.CommentWithAuthor) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows)*2)
	for i := range rows {
		ids = append(ids, rows[i].UserID, rows[i].ReplyToUserID)
	}
	users := loadUsers(ctx, provider, ids)
	for i := range rows {
		if u, ok := users[rows[i].UserID]; ok {
			rows[i].Username = u.Username
			rows[i].Nickname = u.Nickname
			rows[i].Avatar = u.Avatar
		}
		if u, ok := users[rows[i].ReplyToUserID]; ok {
			rows[i].ReplyToNickname = u.Nickname
		}
	}
}

// fillAdminCommentUsers 批量补全管理端评论行的作者与被回复者展示信息。
func fillAdminCommentUsers(ctx context.Context, provider UserProvider, rows []repository.AdminCommentRow) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows)*2)
	for i := range rows {
		ids = append(ids, rows[i].UserID, rows[i].ReplyToUserID)
	}
	users := loadUsers(ctx, provider, ids)
	for i := range rows {
		if u, ok := users[rows[i].UserID]; ok {
			rows[i].Nickname = u.Nickname
			rows[i].Avatar = u.Avatar
		}
		if u, ok := users[rows[i].ReplyToUserID]; ok {
			rows[i].ReplyToNickname = u.Nickname
		}
	}
}

// fillDanmakuUsers 批量补全弹幕行的发送者展示信息。
func fillDanmakuUsers(ctx context.Context, provider UserProvider, rows []repository.DanmakuWithAuthor) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].UserID)
	}
	users := loadUsers(ctx, provider, ids)
	for i := range rows {
		if u, ok := users[rows[i].UserID]; ok {
			rows[i].Username = u.Username
			rows[i].Nickname = u.Nickname
			rows[i].Avatar = u.Avatar
		}
	}
}

// fillAdminDanmakuUsers 批量补全管理端弹幕行的发送者昵称。
func fillAdminDanmakuUsers(ctx context.Context, provider UserProvider, rows []repository.AdminDanmakuRow) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].UserID)
	}
	users := loadUsers(ctx, provider, ids)
	for i := range rows {
		if u, ok := users[rows[i].UserID]; ok {
			rows[i].Nickname = u.Nickname
		}
	}
}

// fillNotificationUsers 批量补全通知行的触发者展示信息。
func fillNotificationUsers(ctx context.Context, provider UserProvider, rows []repository.NotificationWithActor) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ActorID)
	}
	users := loadUsers(ctx, provider, ids)
	for i := range rows {
		if u, ok := users[rows[i].ActorID]; ok {
			rows[i].Nickname = u.Nickname
			rows[i].Avatar = u.Avatar
		}
	}
}
