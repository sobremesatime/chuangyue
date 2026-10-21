// 用户头像上传：写入 Storage（local 落磁盘 / minio 进桶）+ 更新 user.avatar。
//
// 走 pkg/storage 抽象而不是直接写磁盘：头像是对外展示资源，需要能被任意副本读到。
// 写死本地盘会把 user 服务钉在同一节点（local-path 是 RWO），
// 切到对象存储后 user 才能像 web / feed 一样自由调度。
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/user/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

// avatarObjectPrefix 是头像在存储中的 key 前缀。
//
// 单独一层目录而不是扔在桶根：便于按类型统计容量、设生命周期规则，
// 也让桶根留给封面等其他对象，避免混在一起难以辨认。
const avatarObjectPrefix = "avatar/"

// avatarMaxSizeMB 是头像大小上限：头像只用于小尺寸展示，不放开大图。
const avatarMaxSizeMB = 2

// avatarExts 是头像允许的扩展名，与前端 accept 及契约描述保持一致。
var avatarExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// avatarContentTypes 把扩展名映射为 Content-Type。
//
// 显式指定而不是交给驱动猜：对象存储按 Content-Type 决定浏览器怎么渲染，
// 猜错时图片会变成「下载」而不是直接显示。
var avatarContentTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

// UploadAvatar 保存头像文件并更新当前用户的 avatar 字段。
func (s *Service) UploadAvatar(
	ctx context.Context,
	userID int64,
	in dto.UploadAvatarInput,
) (*dto.UserInfo, error) {
	u, err := s.repo.GetByID(ctx, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}

	ext, err := validateAvatarFile(in.Filename, in.Size)
	if err != nil {
		return nil, err
	}

	url, err := s.saveAvatar(ctx, in.File, ext)
	if err != nil {
		return nil, err
	}

	// 落库失败要把刚写入的对象删掉，避免留下没人引用的孤儿文件。
	if err := s.repo.UpdateAvatar(ctx, userID, url); err != nil {
		s.removeAvatar(ctx, url)
		return nil, err
	}

	// 库已指向新头像，旧对象才可删。顺序不能反：先删后写一旦更新失败，
	// 用户的头像就变成一个指向已删对象的死链。
	if u.Avatar != "" && u.Avatar != url {
		s.removeAvatar(ctx, u.Avatar)
	}

	u.Avatar = url
	return toUserInfo(u), nil
}

// validateAvatarFile 校验头像的扩展名与大小，返回规范化后的扩展名。
func validateAvatarFile(filename string, size int64) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	for _, allowed := range avatarExts {
		if ext != "" && ext == allowed {
			if size > int64(avatarMaxSizeMB)*1024*1024 {
				return "", errcode.ErrFileTooLarge.WithMsg(
					fmt.Sprintf("头像不能超过 %d MB", avatarMaxSizeMB),
				)
			}
			return ext, nil
		}
	}
	return "", errcode.ErrFileTypeDenied.WithMsg("头像仅支持 JPG / PNG / WebP 格式")
}

// saveAvatar 把头像写入存储，返回可访问的 URL。
//
// 先整段读进内存而不是直接流式写入：对象存储的单次写入（PutObject）需要准确的
// 字节数，而 in.Size 是客户端声明的，不可信；头像上限只有 2MB，读内存的代价可忽略。
// 顺带用真实长度再做一次上限校验，防止声明值与实际内容不符。
func (s *Service) saveAvatar(ctx context.Context, r io.Reader, ext string) (string, error) {
	limit := int64(avatarMaxSizeMB)*1024*1024 + 1
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return "", fmt.Errorf("读取头像内容失败: %w", err)
	}
	if int64(len(data)) > int64(avatarMaxSizeMB)*1024*1024 {
		return "", errcode.ErrFileTooLarge.WithMsg(
			fmt.Sprintf("头像不能超过 %d MB", avatarMaxSizeMB),
		)
	}

	key := avatarObjectPrefix + strconv.FormatInt(snowflake.NextID(), 10) + ext
	if err := s.store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), avatarContentTypes[ext]); err != nil {
		return "", fmt.Errorf("写入头像失败: %w", err)
	}
	return s.store.URL(key), nil
}

// removeAvatar 删除头像对象；URL 不属于当前驱动时静默跳过。
//
// 跳过是必要的：切换 upload.driver 后，库里可能还留着另一种驱动的地址
// （比如 local 时代的 /uploads/xxx.png），按当前驱动反解会失败。
// 这类历史文件不在这里处理，留给运维按需清理，删除失败也只告警不阻断业务。
func (s *Service) removeAvatar(ctx context.Context, url string) {
	key, ok := s.store.KeyFromURL(url)
	if !ok {
		return
	}
	if err := s.store.Delete(ctx, key); err != nil {
		logger.L().Warn("删除头像对象失败", zap.String("url", url), zap.Error(err))
	}
}
