// Package model 定义用户域的数据模型（与 user 表一一对应）。
package model

import "time"

// 用户角色：两级 RBAC，普通用户只能消费内容，管理员额外可管理视频。
const (
	RoleUser  int8 = 0 // 普通用户
	RoleAdmin int8 = 1 // 管理员
)

// 用户状态。
const (
	StatusDisabled int8 = 0 // 已禁用
	StatusNormal   int8 = 1 // 正常
)

// 用户性别。
const (
	GenderUnset  int8 = 0 // 未设置
	GenderMale   int8 = 1 // 男
	GenderFemale int8 = 2 // 女
	GenderSecret int8 = 3 // 保密
)

// User 对应用户表。
type User struct {
	ID           int64      `gorm:"column:id;primaryKey"`
	Username     string     `gorm:"column:username;size:32;uniqueIndex"`
	Email        string     `gorm:"column:email;size:128;not null;default:'';uniqueIndex"`
	PasswordHash string     `gorm:"column:password_hash;size:100"`
	Nickname     string     `gorm:"column:nickname;size:32"`
	Avatar       string     `gorm:"column:avatar;size:255"`
	Signature    string     `gorm:"column:signature;size:140"`
	Gender       int8       `gorm:"column:gender"`
	Birthday     *time.Time `gorm:"column:birthday;type:date"`
	School       string     `gorm:"column:school;size:64"`
	Location     string     `gorm:"column:location;size:64"`
	Role         int8       `gorm:"column:role"`
	Status       int8       `gorm:"column:status"`
	// TokenVersion 是令牌版本：签发令牌时写进 claims，重置密码时 +1，
	// 刷新时比对，从而让重置前的刷新令牌全部失效（详见 migrations/0016）。
	TokenVersion int       `gorm:"column:token_version"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

// TableName 显式指定表名，避免依赖 GORM 的命名推断。
func (User) TableName() string {
	return "user"
}

// IsAdmin 判断是否为管理员。
func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

// IsNormal 判断账号是否处于正常状态。
func (u *User) IsNormal() bool {
	return u.Status == StatusNormal
}
