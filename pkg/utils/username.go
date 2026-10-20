package utils

import "strings"

const (
	// derivedUsernameMaxLen 是派生用户名的最大长度。
	// 比 username 列的 32 留有余量，给「冲突后追加后缀」留位置。
	derivedUsernameMaxLen = 24
	// derivedUsernameMinLen 与注册接口对用户名的长度要求保持一致。
	derivedUsernameMinLen = 3
	// usernameSuffixLen 是冲突重试时随机后缀的长度。
	usernameSuffixLen = 4
)

// DeriveUsername 由邮箱前缀派生一个合法用户名。
//
// 规则：取 @ 前部分 -> 转小写 -> 只保留 [a-z0-9_] -> 截断到 24 字符；
// 结果不足 3 字符时用 cy + 6 位随机数字兜底。
// 本函数不处理重名，重名由 service 层配合 uk_username 唯一键重试解决。
func DeriveUsername(email string) string {
	local := email
	if at := strings.Index(local, "@"); at >= 0 {
		local = local[:at]
	}

	var b strings.Builder
	for _, r := range strings.ToLower(local) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}

	name := b.String()
	if len(name) > derivedUsernameMaxLen {
		name = name[:derivedUsernameMaxLen]
	}
	if len(name) < derivedUsernameMinLen {
		if code, err := NumericCode(6); err == nil {
			return "cy" + code
		}
		return "cy" + HexSuffix(6)
	}
	return name
}

// NormalizeUsername 归一化用户自定义的用户名：去掉首尾空白并转小写。
//
// 转小写是为了对齐 user 表的 utf8mb4_unicode_ci 排序规则（大小写不敏感），
// 否则出现「数据库判为重名、界面上却像两个名字」的迷惑情况。
func NormalizeUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// IsValidUsername 校验自定义用户名是否合法。
//
// 规则与 DeriveUsername 产出的字符集一致：长度 3–24，必须以小写字母开头，
// 其余字符只能是小写字母、数字或下划线；非 ASCII 字符一律拒绝。
func IsValidUsername(name string) bool {
	if len(name) < derivedUsernameMinLen || len(name) > derivedUsernameMaxLen {
		return false
	}
	for i, r := range name {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if i == 0 {
			if !isLower {
				return false
			}
			continue
		}
		if !isLower && !isDigit && r != '_' {
			return false
		}
	}
	return true
}

// SuffixUsername 在用户名冲突时生成候选：base + "_" + 4 位随机十六进制。
//
// 每次调用都重新取随机后缀，因此重试之间不会产生同一个候选。
func SuffixUsername(base string) string {
	maxBase := derivedUsernameMaxLen - usernameSuffixLen - 1
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	return base + "_" + HexSuffix(usernameSuffixLen)
}
