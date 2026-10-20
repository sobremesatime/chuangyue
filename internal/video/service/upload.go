package service

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/video/dto"
	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/storage"
)

// defaultChunkSizeMB 是未配置分片大小时使用的默认单片大小。
const defaultChunkSizeMB = 8

// maxPartTotal 是分片数量的上限，避免单片过小把分片表撑爆。
const maxPartTotal = 10000

// sessionTTL 是上传会话的有效期，供断点续传与过期清理使用。
const sessionTTL = 24 * time.Hour

// presignTTL 是分片预签名直传地址的有效期。
const presignTTL = 15 * time.Minute

// InitUpload 初始化分片上传：命中秒传直接登记视频，命中上传中的会话则回传进度，
// 否则新建分片会话。
func (s *Service) InitUpload(ctx context.Context, adminID int64, in dto.InitUploadRequest) (*dto.InitUploadResponse, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("标题不能为空")
	}

	md5, err := normalizeMD5(in.FileMD5)
	if err != nil {
		return nil, err
	}

	ext, err := s.validateExt(in.Filename, s.cfg.AllowedExt)
	if err != nil {
		return nil, err
	}
	if err := s.validateSize(in.TotalSize); err != nil {
		return nil, err
	}

	meta := uploadMeta{
		Title:       title,
		Description: strings.TrimSpace(in.Description),
		CategoryID:  in.CategoryID,
	}

	// 秒传：已有相同指纹的已完成会话，直接复用其对象登记视频。
	done, err := s.repo.GetSessionByMD5(ctx, md5, model.UploadStatusCompleted)
	if err == nil {
		// 秒传不传字节，但同样要登记视频并投递转码任务——HLS 产物按 video_id 分目录，
		// 复用源文件也得重新转一遍，所以它照样占一个队列槽位。因此与单文件上传 /
		// 分片收尾保持同一口径，也过背压闸门（见 reserveTranscodeSlot）。
		// 预约只持有到本次收尾结束：秒传没有落盘阶段，持有期极短。
		release, err := s.reserveTranscodeSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()

		info, err := s.finalizeSource(ctx, adminID, s.store.URL(done.StorageKey), meta)
		if err != nil {
			return nil, err
		}
		return &dto.InitUploadResponse{
			Mode:    dto.UploadModeInstant,
			VideoID: strconv.FormatInt(info.ID, 10),
		}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// 断点续传：存在上传中的会话，直接回传其进度，让前端跳过已传分片。
	uploading, err := s.repo.GetSessionByMD5(ctx, md5, model.UploadStatusUploading)
	if err == nil {
		return s.sessionResponse(ctx, uploading)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// 新建会话：按分片大小计算分片总数，并让驱动预分配上传会话。
	chunkSize := int64(s.cfg.ChunkSizeMB) << 20
	if s.cfg.ChunkSizeMB <= 0 {
		chunkSize = defaultChunkSizeMB << 20
	}
	partTotal := int(math.Ceil(float64(in.TotalSize) / float64(chunkSize)))
	if partTotal <= 0 || partTotal > maxPartTotal {
		return nil, errcode.ErrInvalidParam.WithMsg("文件过大，请调大分片大小")
	}

	key := "video/source/" + md5 + ext
	mp, err := s.store.Multipart()
	if err != nil {
		return nil, errcode.ErrStorageUnsupported
	}
	uploadID, err := mp.InitMultipart(ctx, key, in.ContentType)
	if err != nil {
		return nil, errcode.ErrUploadFailed.WithMsg("初始化分片上传失败")
	}

	session := &model.UploadSession{
		ID:          snowflake.NextID(),
		UserID:      adminID,
		FileMD5:     md5,
		Filename:    in.Filename,
		ContentType: in.ContentType,
		Title:       title,
		Description: strings.TrimSpace(in.Description),
		CategoryID:  in.CategoryID,
		TotalSize:   in.TotalSize,
		ChunkSize:   chunkSize,
		PartTotal:   partTotal,
		StorageKey:  key,
		Driver:      s.store.Driver(),
		UploadID:    uploadID,
		Status:      model.UploadStatusUploading,
		ExpiresAt:   time.Now().Add(sessionTTL),
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		// 建会话失败：回滚驱动侧的分片会话，避免残留临时分片。
		_ = mp.AbortMultipart(ctx, key, uploadID)
		return nil, err
	}
	return s.sessionResponse(ctx, session)
}

// UploadStatus 查询上传会话的进度与已上传分片。
func (s *Service) UploadStatus(ctx context.Context, adminID, uploadID int64) (*dto.InitUploadResponse, error) {
	session, err := s.loadOwnedSession(ctx, adminID, uploadID)
	if err != nil {
		return nil, err
	}
	return s.sessionResponse(ctx, session)
}

// PresignPart 返回某一片的预签名直传地址；驱动不支持时返回 ErrStorageUnsupported。
func (s *Service) PresignPart(ctx context.Context, adminID, uploadID int64, number int32) (*dto.PresignPartResponse, error) {
	session, err := s.loadOwnedSession(ctx, adminID, uploadID)
	if err != nil {
		return nil, err
	}
	if number < 1 || int(number) > session.PartTotal {
		return nil, errcode.ErrUploadInvalidPart
	}

	mp, err := s.store.Multipart()
	if err != nil {
		return nil, errcode.ErrStorageUnsupported
	}
	url, err := mp.PresignPart(ctx, session.StorageKey, session.UploadID, number, presignTTL)
	if err != nil {
		if errors.Is(err, storage.ErrNotSupported) {
			return nil, errcode.ErrStorageUnsupported
		}
		return nil, err
	}
	return &dto.PresignPartResponse{PartNumber: number, URL: url}, nil
}

// PutPart 服务端代传一个分片：校验大小后写存储并记录该片。
func (s *Service) PutPart(ctx context.Context, adminID, uploadID int64, number int32, r io.Reader, size int64) (*dto.UploadedPart, error) {
	session, err := s.loadOwnedSession(ctx, adminID, uploadID)
	if err != nil {
		return nil, err
	}
	if number < 1 || int(number) > session.PartTotal {
		return nil, errcode.ErrUploadInvalidPart
	}
	// 非末片必须满片，末片允许小于等于单片大小。
	if number < int32(session.PartTotal) {
		if size != session.ChunkSize {
			return nil, errcode.ErrUploadInvalidPart
		}
	} else if size > session.ChunkSize {
		return nil, errcode.ErrUploadInvalidPart
	}

	mp, err := s.store.Multipart()
	if err != nil {
		return nil, errcode.ErrStorageUnsupported
	}
	etag, err := mp.UploadPart(ctx, session.StorageKey, session.UploadID, number, r, size)
	if err != nil {
		return nil, errcode.ErrUploadFailed
	}

	part := &model.UploadPart{
		SessionID:  session.ID,
		PartNumber: number,
		ETag:       etag,
		Size:       size,
	}
	if err := s.repo.UpsertPart(ctx, part); err != nil {
		return nil, err
	}
	return &dto.UploadedPart{PartNumber: number, ETag: etag, Size: size}, nil
}

// CompleteUpload 合并全部分片并登记视频：先校验分片齐全，再合并、收尾、写回会话状态。
func (s *Service) CompleteUpload(ctx context.Context, adminID, uploadID int64, in dto.CompleteUploadRequest) (*dto.VideoInfo, error) {
	session, err := s.loadOwnedSession(ctx, adminID, uploadID)
	if err != nil {
		return nil, err
	}

	parts, err := s.repo.ListParts(ctx, session.ID)
	if err != nil {
		return nil, err
	}
	if !partsComplete(parts, session.PartTotal) {
		return nil, errcode.ErrUploadNotComplete
	}

	// 背压：分片都传完了才知道队列满，还好——但别再做一次耗时的「合并大文件」。
	// 与单文件上传保持同一口径：预约一个槽位并持有到收尾（见 reserveTranscodeSlot），
	// 这样多个上传同时完成时不会各自读到同一份余量一起放行。
	release, err := s.reserveTranscodeSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	storageParts := make([]storage.Part, 0, len(parts))
	for _, p := range parts {
		storageParts = append(storageParts, storage.Part{Number: p.PartNumber, ETag: p.ETag, Size: p.Size})
	}

	mp, err := s.store.Multipart()
	if err != nil {
		return nil, errcode.ErrStorageUnsupported
	}
	if err := mp.CompleteMultipart(ctx, session.StorageKey, session.UploadID, storageParts); err != nil {
		return nil, errcode.ErrUploadFailed.WithMsg("合并分片失败")
	}

	info, err := s.finalizeSource(ctx, adminID, s.store.URL(session.StorageKey), uploadMeta{
		Title:       session.Title,
		Description: session.Description,
		CategoryID:  session.CategoryID,
		CoverURL:    "",
	})
	if err != nil {
		// 收尾失败：删掉已合并的对象并中止会话，避免留下无主文件。
		_ = s.store.Delete(ctx, session.StorageKey)
		_ = s.repo.UpdateSessionFields(ctx, session.ID, map[string]any{"status": model.UploadStatusAborted})
		return nil, err
	}

	if err := s.repo.UpdateSessionFields(ctx, session.ID, map[string]any{
		"status":   model.UploadStatusCompleted,
		"video_id": info.ID,
	}); err != nil {
		return nil, err
	}
	return info, nil
}

// AbortUpload 中止上传：释放驱动侧分片并把会话标记为已中止。
func (s *Service) AbortUpload(ctx context.Context, adminID, uploadID int64) error {
	session, err := s.loadOwnedSession(ctx, adminID, uploadID)
	if err != nil {
		return err
	}

	mp, err := s.store.Multipart()
	if err != nil {
		return errcode.ErrStorageUnsupported
	}
	if err := mp.AbortMultipart(ctx, session.StorageKey, session.UploadID); err != nil {
		return err
	}
	return s.repo.UpdateSessionFields(ctx, session.ID, map[string]any{"status": model.UploadStatusAborted})
}

// loadOwnedSession 加载上传会话并校验归属与状态：只有会话所有者、且仍在上传中才能操作。
func (s *Service) loadOwnedSession(ctx context.Context, adminID, uploadID int64) (*model.UploadSession, error) {
	session, err := s.repo.GetSession(ctx, uploadID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrUploadSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if session.UserID != adminID {
		return nil, errcode.ErrForbidden
	}
	if session.Status != model.UploadStatusUploading {
		return nil, errcode.ErrUploadSessionNotFound
	}
	return session, nil
}

// sessionResponse 把上传会话转换为初始化 / 查询的响应，并附带已上传分片。
func (s *Service) sessionResponse(ctx context.Context, session *model.UploadSession) (*dto.InitUploadResponse, error) {
	parts, err := s.repo.ListParts(ctx, session.ID)
	if err != nil {
		return nil, err
	}

	uploaded := make([]dto.UploadedPart, 0, len(parts))
	for _, p := range parts {
		uploaded = append(uploaded, dto.UploadedPart{PartNumber: p.PartNumber, ETag: p.ETag, Size: p.Size})
	}
	return &dto.InitUploadResponse{
		Mode:      dto.UploadModeMultipart,
		UploadID:  strconv.FormatInt(session.ID, 10),
		ChunkSize: session.ChunkSize,
		PartTotal: session.PartTotal,
		Uploaded:  uploaded,
	}, nil
}

// normalizeMD5 校验并规范化整文件指纹：必须是 32 位十六进制，统一转小写。
func normalizeMD5(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if _, err := hex.DecodeString(v); err != nil || len(v) != 32 {
		return "", errcode.ErrInvalidParam.WithMsg("文件指纹不合法")
	}
	return v, nil
}

// partsComplete 校验已上传分片是否完整覆盖 1..partTotal 的全部序号。
func partsComplete(parts []model.UploadPart, partTotal int) bool {
	if partTotal <= 0 || len(parts) != partTotal {
		return false
	}
	seen := make([]bool, partTotal+1)
	for _, p := range parts {
		if p.PartNumber < 1 || int(p.PartNumber) > partTotal || seen[int(p.PartNumber)] {
			return false
		}
		seen[int(p.PartNumber)] = true
	}
	return true
}
