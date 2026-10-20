//go:build integration

// 互动域「作者 / 触发者信息来自 UserProvider 而非仓储 JOIN user」的集成测试：需要本地 MySQL。
//
// 用一个返回固定昵称的 stub UserProvider 构造评论服务，断言返回的昵称等于 stub 的值，
// 直接证明昵称不再来自数据库的 user 表、跨服务边界已被切开；同时用真实 Provider 适配器
// 验证「作者已注销 / 不存在」时行仍保留、昵称头像留空。依赖不可用时跳过（不判失败）：
//
//	go test -tags integration -v ./internal/interaction/handler/...
package handler

import (
	"context"
	"testing"

	"gorm.io/gorm"

	interactiondto "github.com/sobremesatime/chuangyue/internal/interaction/dto"
	interactionmodel "github.com/sobremesatime/chuangyue/internal/interaction/model"
	interactionrepo "github.com/sobremesatime/chuangyue/internal/interaction/repository"
	"github.com/sobremesatime/chuangyue/internal/interaction/service"
	userrepo "github.com/sobremesatime/chuangyue/internal/user/repository"
	usersvc "github.com/sobremesatime/chuangyue/internal/user/service"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

// stubUserProvider 是返回固定昵称 / 头像的 UserProvider，用来证明昵称并非取自 user 表。
type stubUserProvider struct {
	nickname string
	avatar   string
}

func (s stubUserProvider) GetUsers(_ context.Context, ids []int64) (map[int64]service.UserBrief, error) {
	out := make(map[int64]service.UserBrief, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		out[id] = service.UserBrief{ID: id, Username: "stub-user", Nickname: s.nickname, Avatar: s.avatar}
	}
	return out, nil
}

// stubVideoProvider 是「任何 id 都视为存在的已上架视频」的 VideoProvider，
// 用于在拆库后替掉原先仓储对视频表的直读（评论列表只需通过存在性校验）。
type stubVideoProvider struct{}

func (stubVideoProvider) GetVideo(_ context.Context, id int64) (*interactiondto.VideoInfo, error) {
	if id <= 0 {
		return nil, nil
	}
	return &interactiondto.VideoInfo{ID: id, Status: videomodel.StatusPublished}, nil
}

func (stubVideoProvider) GetVideos(_ context.Context, ids []int64) (map[int64]interactiondto.VideoInfo, error) {
	out := make(map[int64]interactiondto.VideoInfo, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		out[id] = interactiondto.VideoInfo{ID: id, Status: videomodel.StatusPublished}
	}
	return out, nil
}

// localUserProviderAdapter 是网关侧适配器的测试内联版：用用户服务的本地查询实现 UserProvider。
type localUserProviderAdapter struct {
	query *usersvc.QueryService
}

func (a localUserProviderAdapter) GetUsers(ctx context.Context, ids []int64) (map[int64]service.UserBrief, error) {
	users, err := a.query.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]service.UserBrief, len(users))
	for i := range users {
		out[users[i].ID] = service.UserBrief{
			ID:       users[i].ID,
			Username: users[i].Username,
			Nickname: users[i].Nickname,
			Avatar:   users[i].Avatar,
		}
	}
	return out, nil
}

// seedCommentFixture 造一条临时视频与一条临时评论，返回 videoID 与 commentID，
// 并注册精确清理（只删自己插入的两行）。
func seedCommentFixture(t *testing.T, db *gorm.DB, authorID int64) (int64, int64) {
	t.Helper()

	videoID := snowflake.NextID()
	if err := db.Create(&videomodel.Video{
		ID:     videoID,
		Title:  "userprovider-it-video",
		Status: videomodel.StatusPublished,
	}).Error; err != nil {
		t.Fatalf("插入临时视频失败: %v", err)
	}
	t.Cleanup(func() { db.Delete(&videomodel.Video{}, videoID) })

	commentID := snowflake.NextID()
	if err := db.Create(&interactionmodel.VideoComment{
		ID:      commentID,
		VideoID: videoID,
		UserID:  authorID,
		Content: "userprovider-it-comment",
		Status:  interactionmodel.CommentStatusNormal,
	}).Error; err != nil {
		t.Fatalf("插入临时评论失败: %v", err)
	}
	t.Cleanup(func() { db.Delete(&interactionmodel.VideoComment{}, commentID) })

	return videoID, commentID
}

// findComment 在列表里按 id 找评论。
func findComment(list []interactiondto.CommentInfo, id int64) (interactiondto.CommentInfo, bool) {
	for i := range list {
		if list[i].ID == id {
			return list[i], true
		}
	}
	return interactiondto.CommentInfo{}, false
}

// TestCommentAuthorComesFromUserProvider 证明评论昵称头像来自 UserProvider，而非仓储 JOIN user。
func TestCommentAuthorComesFromUserProvider(t *testing.T) {
	db := newTestDB(t)
	repo := interactionrepo.New(db)

	// 评论作者用一个必定不存在的 user id，确保 user 表里没有对应行：
	// 若昵称仍取自 user 表，这里只会是空串，断言 stub 的固定值即可证伪。
	authorID := int64(1 << 61)
	videoID, commentID := seedCommentFixture(t, db, authorID)

	t.Run("stub_provider", func(t *testing.T) {
		const stubNick = "stub-provider-nick"
		stub := stubUserProvider{nickname: stubNick, avatar: "stub-provider-avatar"}
		svc := service.NewCommentService(repo, nil, stub, stubVideoProvider{})

		list, _, _, _, err := svc.List(context.Background(), videoID, 0, 1, 100)
		if err != nil {
			t.Fatalf("评论列表失败: %v", err)
		}
		got, ok := findComment(list, commentID)
		if !ok {
			t.Fatalf("列表未返回临时评论 %d", commentID)
		}
		if got.Nickname != stubNick {
			t.Fatalf("昵称应来自 stub 提供者 %q，实际 %q（说明仍在读 user 表）", stubNick, got.Nickname)
		}
		if got.Username != "stub-user" {
			t.Fatalf("用户名应来自 stub 提供者，实际 %q", got.Username)
		}
	})

	// 真实 Provider 适配器：作者 user 行缺失时应保留该行、昵称头像留空（与 LEFT JOIN 的容忍一致）。
	t.Run("real_provider_absent_user", func(t *testing.T) {
		real := localUserProviderAdapter{query: usersvc.NewQueryService(userrepo.New(db))}
		svc := service.NewCommentService(repo, nil, real, stubVideoProvider{})

		list, _, _, _, err := svc.List(context.Background(), videoID, 0, 1, 100)
		if err != nil {
			t.Fatalf("评论列表失败: %v", err)
		}
		got, ok := findComment(list, commentID)
		if !ok {
			t.Fatalf("作者不存在时评论不应被丢行，实际未返回 %d", commentID)
		}
		if got.Nickname != "" || got.Avatar != "" {
			t.Fatalf("作者不存在时昵称头像应留空，实际 nickname=%q avatar=%q", got.Nickname, got.Avatar)
		}
	})
}
