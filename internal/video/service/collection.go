package service

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/internal/video/repository"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// 合集字段长度上限，与迁移里的列定义一一对应。
//
// 在这里先校验而不是交给 MySQL：严格模式下超长会直接报错，非严格模式下会被静默截断，
// 两种结果都不该让用户看到。提前给出一句「标题过长」比 500 或悄悄丢字友好。
const (
	collectionTitleMaxLen = 128
	collectionDescMaxLen  = 512
	collectionCoverMaxLen = 255
)

// GetCollection 返回公开合集的详情与其中已上架的视频，供前台播放列表使用。
//
// 草稿合集在公开读里等同不存在（返回「合集不存在」而不是「无权访问」），
// 避免把「有这样一个草稿」的事实暴露出去。
func (s *Service) GetCollection(ctx context.Context, id int64) (*dto.CollectionDetail, error) {
	return s.collectionDetail(ctx, id, true)
}

// AdminGetCollection 返回任意状态的合集详情与其中全部视频，供管理端编排排序使用。
//
// 与公开读的差别有两处：不拒绝草稿合集、不丢弃未上架视频。
// 管理端刚加进合集的视频往往还在转码，若沿用公开口径，排序面板里的顺序
// 会与库内真实顺序对不上，重排时就会被判定为「集合不一致」。
func (s *Service) AdminGetCollection(ctx context.Context, id int64) (*dto.CollectionDetail, error) {
	return s.collectionDetail(ctx, id, false)
}

// collectionDetail 是公开读与管理端读的共同实现。
//
// onlyPublic 为真时：草稿合集按不存在处理，并丢弃未上架视频；
// 为假时：合集与视频的状态都不做过滤。
func (s *Service) collectionDetail(ctx context.Context, id int64, onlyPublic bool) (*dto.CollectionDetail, error) {
	c, err := s.repo.GetCollectionByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrNotFound.WithMsg("合集不存在")
	}
	if err != nil {
		return nil, err
	}
	if onlyPublic && c.Status != model.CollectionStatusPublic {
		return nil, errcode.ErrNotFound.WithMsg("合集不存在")
	}

	links, err := s.repo.ListCollectionVideos(ctx, id)
	if err != nil {
		return nil, err
	}

	videos := make([]dto.VideoInfo, 0, len(links))
	if len(links) > 0 {
		ids := make([]int64, 0, len(links))
		for _, link := range links {
			ids = append(ids, link.VideoID)
		}
		all, err := s.repo.GetByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		byID := make(map[int64]*model.Video, len(all))
		for i := range all {
			byID[all[i].ID] = &all[i]
		}

		// 按关联行的顺序遍历，而不是遍历查询结果：播放列表的顺序来自 (sort, id)，
		// 批量查询的返回顺序不保证有序；同时在这里按需丢掉未上架的视频。
		for _, link := range links {
			v, ok := byID[link.VideoID]
			if !ok || (onlyPublic && v.Status != model.StatusPublished) {
				continue
			}
			videos = append(videos, *toVideoInfo(v))
		}
	}

	info := toCollectionInfo(c)
	if info.CoverURL == "" && len(videos) > 0 {
		// 封面兜底只在读取侧发生：写进库会把它固化成快照，视频换封面后合集封面就不跟随了。
		info.CoverURL = videos[0].CoverURL
	}
	return &dto.CollectionDetail{CollectionInfo: *info, Videos: videos}, nil
}

// GetCollectionByVideo 返回包含该视频的公开合集，并给出该视频在合集公开列表中的序号。
//
// 序号按「已上架视频」的 (sort, id) 顺序计算，与 GetCollection 返回的播放列表口径一致——
// 若把下架视频也计入，前端「第 N 个」就会指到列表里并不存在的条目。
func (s *Service) GetCollectionByVideo(ctx context.Context, videoID int64) ([]dto.CollectionBrief, error) {
	collections, err := s.repo.ListPublicCollectionsByVideo(ctx, videoID)
	if err != nil {
		return nil, err
	}
	briefs := make([]dto.CollectionBrief, 0, len(collections))
	if len(collections) == 0 {
		return briefs, nil
	}

	collectionIDs := make([]int64, 0, len(collections))
	for i := range collections {
		collectionIDs = append(collectionIDs, collections[i].ID)
	}
	links, err := s.repo.ListCollectionVideosByCollections(ctx, collectionIDs)
	if err != nil {
		return nil, err
	}

	// PublishedCovers 的键即「已上架视频」，一次查询同时拿到可见性判断与封面。
	videoIDs := make([]int64, 0, len(links))
	for _, link := range links {
		videoIDs = append(videoIDs, link.VideoID)
	}
	covers, err := s.repo.PublishedCovers(ctx, videoIDs)
	if err != nil {
		return nil, err
	}

	linksByCollection := make(map[int64][]model.CollectionVideo, len(collectionIDs))
	for _, link := range links {
		linksByCollection[link.CollectionID] = append(linksByCollection[link.CollectionID], link)
	}

	for _, c := range collections {
		// 关联行已按 (sort, id) 升序返回，同一个合集的行在切片里也是有序的。
		index, current := 0, 0
		firstCover, hasFirstCover := "", false
		for _, link := range linksByCollection[c.ID] {
			cover, published := covers[link.VideoID]
			if !published {
				continue
			}
			index++
			if !hasFirstCover {
				firstCover, hasFirstCover = cover, true
			}
			if link.VideoID == videoID {
				current = index
			}
		}

		brief := dto.CollectionBrief{
			ID:           c.ID,
			Title:        c.Title,
			CoverURL:     c.CoverURL,
			VideoCount:   c.VideoCount,
			CurrentIndex: current,
		}
		if brief.CoverURL == "" {
			brief.CoverURL = firstCover
		}
		briefs = append(briefs, brief)
	}
	return briefs, nil
}

// ListCollections 分页查询公开合集，供前台列表使用。
func (s *Service) ListCollections(ctx context.Context, page, pageSize int) ([]dto.CollectionInfo, int64, int, int, error) {
	return s.listCollections(ctx, true, page, pageSize)
}

// AdminListCollections 分页查询全部合集（含草稿），供管理后台使用。
//
// 与 ListCollections 的唯一差别是不过滤状态；封面兜底同样生效，
// 让管理端列表与前台看到的封面一致。
func (s *Service) AdminListCollections(ctx context.Context, page, pageSize int) ([]dto.CollectionInfo, int64, int, int, error) {
	return s.listCollections(ctx, false, page, pageSize)
}

// listCollections 是公开列表与管理端列表的共同实现。
//
// 分页参数归一复用 utils.NormalizePage，与视频列表口径一致（page<1 取 1、pageSize 封顶 100）。
func (s *Service) listCollections(ctx context.Context, onlyPublic bool, page, pageSize int) ([]dto.CollectionInfo, int64, int, int, error) {
	normPage, normPageSize, offset := utils.NormalizePage(page, pageSize)

	collections, total, err := s.repo.ListCollections(ctx, onlyPublic, offset, normPageSize)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	list := make([]dto.CollectionInfo, 0, len(collections))
	ids := make([]int64, 0, len(collections))
	for i := range collections {
		list = append(list, *toCollectionInfo(&collections[i]))
		ids = append(ids, collections[i].ID)
	}

	// 封面兜底对所有列表生效：管理员没单独设封面时，列表页也该有图可展示。
	// 草稿合集同样能兜底——管理端需要看到它将来的样子。
	if len(ids) > 0 {
		covers, err := s.repo.FirstPublishedCovers(ctx, ids)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		for i := range list {
			if list[i].CoverURL == "" {
				list[i].CoverURL = covers[list[i].ID]
			}
		}
	}
	return list, total, normPage, normPageSize, nil
}

// CreateCollection 新建合集。uploaderID 来自 gRPC metadata 透传的调用者身份。
//
// 新建默认「公开」：管理端建合集的动作本身就是发布意图，需要草稿态时再手动改状态；
// 这比默认草稿更少一次手滑（建完在前台看不到）。
func (s *Service) CreateCollection(ctx context.Context, uploaderID int64, in dto.CreateCollectionInput) (*dto.CollectionInfo, error) {
	if uploaderID <= 0 {
		return nil, errcode.ErrUnauthorized
	}

	title, err := normalizeCollectionTitle(in.Title)
	if err != nil {
		return nil, err
	}
	description, err := validateCollectionText(in.Description, collectionDescMaxLen, "简介")
	if err != nil {
		return nil, err
	}
	coverURL, err := validateCollectionText(in.CoverURL, collectionCoverMaxLen, "封面地址")
	if err != nil {
		return nil, err
	}

	c := &model.Collection{
		ID:          snowflake.NextID(),
		Title:       title,
		Description: description,
		CoverURL:    coverURL,
		UploaderID:  uploaderID,
		Status:      model.CollectionStatusPublic,
	}
	if err := s.repo.CreateCollection(ctx, c); err != nil {
		return nil, err
	}
	return toCollectionInfo(c), nil
}

// UpdateCollection 编辑合集（标题 / 简介 / 封面 / 状态）。
//
// 字段用指针表达「未传」：简介与封面允许清空成空串，未传则表示不修改。
// 状态只接受 0（草稿）与 1（公开）——草稿与公开是本域仅有的两态。
func (s *Service) UpdateCollection(ctx context.Context, id int64, in dto.UpdateCollectionInput) (*dto.CollectionInfo, error) {
	if _, err := s.getCollectionForAdmin(ctx, id); err != nil {
		return nil, err
	}

	fields := make(map[string]any, 4)
	if in.Title != nil {
		title, err := normalizeCollectionTitle(*in.Title)
		if err != nil {
			return nil, err
		}
		fields["title"] = title
	}
	if in.Description != nil {
		description, err := validateCollectionText(*in.Description, collectionDescMaxLen, "简介")
		if err != nil {
			return nil, err
		}
		fields["description"] = description
	}
	if in.CoverURL != nil {
		// 传空串是「恢复用第一个视频封面兜底」的合法操作，不做非空校验。
		coverURL, err := validateCollectionText(*in.CoverURL, collectionCoverMaxLen, "封面地址")
		if err != nil {
			return nil, err
		}
		fields["cover_url"] = coverURL
	}
	if in.Status != nil {
		if *in.Status != model.CollectionStatusDraft && *in.Status != model.CollectionStatusPublic {
			return nil, errcode.ErrInvalidParam.WithMsg("状态只能是 0（草稿）或 1（公开）")
		}
		fields["status"] = *in.Status
	}

	if err := s.repo.UpdateCollectionFields(ctx, id, fields); err != nil {
		return nil, err
	}
	return s.collectionInfo(ctx, id)
}

// DeleteCollection 删除合集及其全部关联行。
//
// 只删合集与关联，不动视频本身：合集是视频的编排视图，删除编排不该连带删内容。
func (s *Service) DeleteCollection(ctx context.Context, id int64) error {
	if _, err := s.getCollectionForAdmin(ctx, id); err != nil {
		return err
	}
	return s.repo.DeleteCollection(ctx, id)
}

// AddCollectionVideos 把视频按传入顺序追加到合集末尾。
//
// 幂等：已在合集内的视频被忽略（不报错、不占新序号）。
// 入参先做「非空 / 不重复 / 存在」三重校验，再交给仓储在事务里追加并重算计数。
func (s *Service) AddCollectionVideos(ctx context.Context, collectionID int64, videoIDs []int64) (*dto.CollectionInfo, error) {
	if _, err := s.getCollectionForAdmin(ctx, collectionID); err != nil {
		return nil, err
	}
	if err := validateCollectionVideoIDs(videoIDs); err != nil {
		return nil, err
	}
	if err := s.ensureVideosExist(ctx, videoIDs); err != nil {
		return nil, err
	}
	if err := s.repo.AddCollectionVideos(ctx, collectionID, videoIDs); err != nil {
		return nil, err
	}
	return s.collectionInfo(ctx, collectionID)
}

// RemoveCollectionVideo 从合集移出一个视频。
//
// 幂等：视频本来就不在合集里时也返回成功（重复点击不该报错），
// 计数照常重算，顺带修正历史上可能的漂移。
func (s *Service) RemoveCollectionVideo(ctx context.Context, collectionID, videoID int64) (*dto.CollectionInfo, error) {
	if _, err := s.getCollectionForAdmin(ctx, collectionID); err != nil {
		return nil, err
	}
	if videoID <= 0 {
		return nil, errcode.ErrInvalidParam.WithMsg("非法的视频 ID")
	}
	if err := s.repo.RemoveCollectionVideo(ctx, collectionID, videoID); err != nil {
		return nil, err
	}
	return s.collectionInfo(ctx, collectionID)
}

// ReorderCollectionVideos 按传入的完整顺序重写合集播放顺序。
//
// 传入的集合必须与库内现有视频完全一致（由仓储在事务内校验），
// 否则返回参数错误——少了任何一个视频都会让「完整顺序」这个语义失效。
func (s *Service) ReorderCollectionVideos(ctx context.Context, collectionID int64, videoIDs []int64) (*dto.CollectionInfo, error) {
	if _, err := s.getCollectionForAdmin(ctx, collectionID); err != nil {
		return nil, err
	}
	if err := validateCollectionVideoIDs(videoIDs); err != nil {
		return nil, err
	}

	err := s.repo.ReorderCollectionVideos(ctx, collectionID, videoIDs)
	if errors.Is(err, repository.ErrCollectionOrderMismatch) {
		return nil, errcode.ErrInvalidParam.WithMsg("传入的视频集合与合集内现有视频不一致")
	}
	if err != nil {
		return nil, err
	}
	return s.collectionInfo(ctx, collectionID)
}

// getCollectionForAdmin 读取合集（含草稿），不存在时翻译成业务错误。
//
// 管理端写操作的前提都是「合集存在」：统一在这里判断，
// 避免每条写路径各写一遍 ErrRecordNotFound 的判断而漏掉某条。
func (s *Service) getCollectionForAdmin(ctx context.Context, id int64) (*model.Collection, error) {
	c, err := s.repo.GetCollectionByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrNotFound.WithMsg("合集不存在")
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// collectionInfo 读取合集当前状态并转成对外结构，供写操作回执使用。
func (s *Service) collectionInfo(ctx context.Context, id int64) (*dto.CollectionInfo, error) {
	c, err := s.getCollectionForAdmin(ctx, id)
	if err != nil {
		return nil, err
	}
	return toCollectionInfo(c), nil
}

// ensureVideosExist 校验待加入的视频全部存在。
//
// 只校验存在性、不限制状态：合集可以先编排「转码中」的视频，等转码完成自动可见；
// 公开读那边会过滤掉未上架的视频，所以这里放开状态是安全的。
func (s *Service) ensureVideosExist(ctx context.Context, videoIDs []int64) error {
	videos, err := s.repo.GetByIDs(ctx, videoIDs)
	if err != nil {
		return err
	}
	if len(videos) != len(videoIDs) {
		return errcode.ErrVideoNotFound.WithMsg("部分视频不存在，请刷新后重试")
	}
	return nil
}

// validateCollectionVideoIDs 校验视频 ID 列表：非空、均为正数、无重复。
//
// 重复项必须拒绝而不是静默去重：重排时重复项会让「完整顺序」的含义变得歧义，
// 添加时也说明前端状态不一致，明确报错比悄悄修正更好排查。
func validateCollectionVideoIDs(videoIDs []int64) error {
	if len(videoIDs) == 0 {
		return errcode.ErrInvalidParam.WithMsg("视频列表不能为空")
	}
	seen := make(map[int64]struct{}, len(videoIDs))
	for _, id := range videoIDs {
		if id <= 0 {
			return errcode.ErrInvalidParam.WithMsg("非法的视频 ID")
		}
		if _, ok := seen[id]; ok {
			return errcode.ErrInvalidParam.WithMsg("视频不能重复")
		}
		seen[id] = struct{}{}
	}
	return nil
}

// normalizeCollectionTitle 去掉首尾空白并校验非空与长度。
func normalizeCollectionTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", errcode.ErrInvalidParam.WithMsg("标题不能为空")
	}
	if len([]rune(title)) > collectionTitleMaxLen {
		return "", errcode.ErrInvalidParam.WithMsg("标题过长")
	}
	return title, nil
}

// validateCollectionText 去空白并校验文本字段长度；空串合法（表示清空或使用兜底）。
func validateCollectionText(raw string, maxLen int, field string) (string, error) {
	text := strings.TrimSpace(raw)
	if len([]rune(text)) > maxLen {
		return "", errcode.ErrInvalidParam.WithMsg(field + "过长")
	}
	return text, nil
}

// toCollectionInfo 把数据模型转换为对外 DTO。
func toCollectionInfo(c *model.Collection) *dto.CollectionInfo {
	return &dto.CollectionInfo{
		ID:          c.ID,
		Title:       c.Title,
		Description: c.Description,
		CoverURL:    c.CoverURL,
		UploaderID:  c.UploaderID,
		Status:      c.Status,
		VideoCount:  c.VideoCount,
		CreatedAt:   c.CreatedAt,
	}
}
