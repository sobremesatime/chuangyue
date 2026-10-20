// Package utils 提供与业务无关的通用工具函数。
package utils

import "golang.org/x/crypto/bcrypt"

// HashPassword 用 bcrypt 对明文密码做哈希。
// bcrypt 自带随机盐，同一密码每次哈希结果不同，无需额外存储盐值。
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword 校验明文密码与哈希是否匹配。
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
