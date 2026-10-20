// 用户头像上传：落本地磁盘 + 更新 user.avatar。
//
// 当前版本与视频、封面共用 upload.local_dir，通过网关注册的 /uploads 静态目录对外提供；
// 后续版本切到 MinIO / CDN 时只需替换这里的落盘实现。
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/user/dto"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
)

// avatarURLPrefix 与路由中注册的静态目录保持一致。
const avatarURLPrefix = "/uploads/"

// avatarMaxSizeMB 是头像大小上限：头像只用于小尺寸展示，不放开大图。
const avatarMaxSizeMB = 2

// avatarExts 是头像允许的扩展名，与前端 accept 及契约描述保持一致。
var avatarExts = []string{".jpg", ".jpeg", ".png", ".webp"}

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

	url, err := s.saveAvatar(in.File, ext)
	if err != nil {
		return nil, err
	}

	// 落库失败要把刚写入的文件删掉，避免留下没人引用的孤儿文件。
	if err := s.repo.UpdateAvatar(ctx, userID, url); err != nil {
		_ = os.Remove(s.avatarLocalPath(url))
		return nil, err
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

// saveAvatar 把头像写入本地磁盘，文件名为雪花 ID，返回可访问的 URL。
func (s *Service) saveAvatar(r io.Reader, ext string) (string, error) {
	dir := s.ucfg.LocalDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建上传目录失败: %w", err)
	}

	name := strconv.FormatInt(snowflake.NextID(), 10) + ext
	dst := filepath.Join(dir, name)

	f, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("创建文件失败: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("写入文件失败: %w", err)
	}
	return avatarURLPrefix + name, nil
}

// avatarLocalPath 把对外 URL 还原为本地磁盘路径，用于失败清理。
func (s *Service) avatarLocalPath(url string) string {
	return filepath.Join(s.ucfg.LocalDir, strings.TrimPrefix(url, avatarURLPrefix))
}
