package utils

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
)

// NumericCode 生成 n 位数字验证码，允许前导零（定长）。
//
// 必须用 crypto/rand 而不是 math/rand：验证码是安全凭证，
// math/rand 的默认种子可预测，等于没有验证码。
func NumericCode(n int) (string, error) {
	if n <= 0 {
		return "", nil
	}

	buf := make([]byte, n)
	ten := big.NewInt(10)
	for i := range buf {
		v, err := rand.Int(rand.Reader, ten)
		if err != nil {
			return "", err
		}
		buf[i] = byte('0' + v.Int64())
	}
	return string(buf), nil
}

// HexSuffix 生成 n 个字符的十六进制随机串，用于用户名冲突时追加随机后缀。
//
// 随机后缀而非自增序号：序号会让外部通过注册探测出「某用户名已被占用」。
func HexSuffix(n int) string {
	if n <= 0 {
		return ""
	}

	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)[:n]
}
