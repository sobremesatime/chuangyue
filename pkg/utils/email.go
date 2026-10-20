package utils

import (
	"net/mail"
	"strings"
)

// maxEmailLen 与 user 表 email 列长度（VARCHAR(128)）保持一致。
const maxEmailLen = 128

// NormalizeEmail 归一化邮箱：去掉首尾空白并统一小写。
//
// 严格来说邮箱的本地部分是大小写敏感的（RFC 5321），但现实中几乎没有
// 服务商区分大小写；统一小写可以避免「Alice@x.com 与 alice@x.com
// 注册出两个账号」这类数据分裂问题。
func NormalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// IsValidEmail 判断邮箱格式是否合法。
//
// 用标准库 net/mail 解析而不是自写正则：正则很难覆盖带引号等合法形式，
// 标准库更可靠。额外约束长度以匹配数据库列宽。
func IsValidEmail(s string) bool {
	if s == "" || len(s) > maxEmailLen {
		return false
	}

	addr, err := mail.ParseAddress(s)
	if err != nil {
		return false
	}
	// ParseAddress 接受 `昵称 <a@b.com>` 形式，这里要求是纯地址，
	// 避免把显示名一起存进数据库。
	if addr.Address != s {
		return false
	}

	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return false
	}
	// 域名必须含点，挡掉 a@localhost 这类写法。
	return strings.Contains(s[at+1:], ".")
}
