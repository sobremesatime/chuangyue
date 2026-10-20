// Package repository 负责用户表的数据访问，只关心「怎么读写数据库」，不掺业务规则。
package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/user/model"
)

// Repository 封装用户表的数据库操作。
type Repository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 插入一条用户记录。
func (r *Repository) Create(ctx context.Context, u *model.User) error {
	return r.db.WithContext(ctx).Create(u).Error
}

// GetByUsername 按用户名查询。未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).Where("username = ?", username).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByID 按主键查询。未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetByID(ctx context.Context, id int64) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByIDs 按主键批量查询用户；入参为空返回空切片，不存在的 id 不占位。
//
// 纯读操作，返回顺序不保证与入参一致，调用方如需顺序应自行按 id 归位。
func (r *Repository) GetByIDs(ctx context.Context, ids []int64) ([]model.User, error) {
	if len(ids) == 0 {
		return []model.User{}, nil
	}
	var list []model.User
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ExistsByUsername 判断用户名是否已存在，用于注册前置校验。
func (r *Repository) ExistsByUsername(ctx context.Context, username string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.User{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}

// GetByEmail 按邮箱查询。未找到时返回 gorm.ErrRecordNotFound。
func (r *Repository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).Where("email = ?", email).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// ExistsByEmail 判断邮箱是否已被注册，用于注册前置校验。
func (r *Repository) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.User{}).Where("email = ?", email).Count(&count).Error
	return count > 0, err
}

// ExistsByUsernameExcept 判断用户名是否被「别人」占用。
//
// 改名时要把自己排除掉：归一化后与原值相同的情况在 service 层已提前跳过，
// 这里再排除一次，避免任何情况下把自己判成重名。
func (r *Repository) ExistsByUsernameExcept(ctx context.Context, username string, excludeID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.User{}).
		Where("username = ? AND id <> ?", username, excludeID).
		Count(&count).Error
	return count > 0, err
}

// UpdateAvatar 只更新头像列，避免整行写入把并发读到的旧值一起覆盖回去。
func (r *Repository) UpdateAvatar(ctx context.Context, id int64, avatarURL string) error {
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).
		Update("avatar", avatarURL).Error
}

// UpdatePassword 只更新密码哈希这一列。
//
// 与 UpdateAvatar 同样的做法：单独写一列，避免把并发读到的旧资料整行覆盖回去。
func (r *Repository) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).
		Update("password_hash", passwordHash).Error
}

// ResetPassword 在一条 UPDATE 里改写密码哈希并把令牌版本 +1。
//
// 两处刻意设计：
//   - 用 gorm.Expr 让 +1 在数据库侧完成，"读版本号再写回"会在并发重置时互相覆盖；
//   - 两者必须同一条语句，否则会留下"新密码已生效、旧刷新令牌仍可用"的中间态。
func (r *Repository) ResetPassword(ctx context.Context, id int64, passwordHash string) error {
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).
		Updates(map[string]any{
			"password_hash": passwordHash,
			"token_version": gorm.Expr("token_version + 1"),
		}).Error
}

// BumpTokenVersion 只推进令牌版本，使该用户此前签发的全部刷新令牌立即失效。
//
// 与 ResetPassword 里的自增同一套机制，区别是不动密码——用于「检测到令牌重放、
// 判定会话已泄露」这类只能靠吊销止损的场景。
func (r *Repository) BumpTokenVersion(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).
		Update("token_version", gorm.Expr("token_version + 1")).Error
}

// UserFilter 是用户管理列表的过滤条件；nil 字段表示该维度不过滤。
type UserFilter struct {
	// Keyword 对用户名 / 邮箱 / 昵称作子串匹配。
	Keyword string
	Role    *int8
	Status  *int8
}

// List 按条件分页查询用户，返回当页数据与满足条件的总数（分页控件需要 total）。
//
// 过滤条件用闭包复用两次：GORM 的 Count 会改写 statement，
// 在同一个 *gorm.DB 上接着 Find 容易把 SELECT 带歪，重建一次最稳。
func (r *Repository) List(ctx context.Context, f UserFilter, offset, limit int) ([]model.User, int64, error) {
	apply := func() *gorm.DB {
		q := r.db.WithContext(ctx).Model(&model.User{})
		if f.Keyword != "" {
			like := "%" + f.Keyword + "%"
			q = q.Where("username LIKE ? OR email LIKE ? OR nickname LIKE ?", like, like, like)
		}
		if f.Role != nil {
			q = q.Where("role = ?", *f.Role)
		}
		if f.Status != nil {
			q = q.Where("status = ?", *f.Status)
		}
		return q
	}

	var total int64
	if err := apply().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []model.User
	// 按雪花 ID 倒序即「最新注册在前」：ID 单调递增，比 created_at 少一次排序代价。
	if err := apply().Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// AdminUpdate 按列更新账号状态 / 角色，updates 的 key 只允许 status 与 role。
func (r *Repository) AdminUpdate(ctx context.Context, id int64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

// UpdateProfile 按列更新个人资料，updates 的 key 必须是白名单里的列名。
//
// 用 map 而不是 struct：GORM 的 Updates(struct) 会跳过零值，
// 那样「清空签名 / 生日置空 / 性别改回 0」都会失效。
func (r *Repository) UpdateProfile(ctx context.Context, id int64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}
