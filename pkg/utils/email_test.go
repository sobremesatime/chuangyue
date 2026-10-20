package utils

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"  Alice@Example.COM  ", "alice@example.com"},
		{"alice@example.com", "alice@example.com"},
		{"ALICE@EXAMPLE.COM", "alice@example.com"},
		{"\talice@example.com\n", "alice@example.com"},
		{"", ""},
	}

	for _, c := range cases {
		if got := NormalizeEmail(c.in); got != c.want {
			t.Errorf("NormalizeEmail(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestIsValidEmail(t *testing.T) {
	valid := []string{
		"alice@example.com",
		"a_b.c+tag@sub.domain.cn",
		"1@2.com",
		"alice@example.co",
	}
	for _, s := range valid {
		if !IsValidEmail(s) {
			t.Errorf("IsValidEmail(%q) = false, 期望 true", s)
		}
	}

	invalid := []string{
		"",
		"alice",
		"alice@",
		"@example.com",
		"alice@localhost",                   // 域名无点
		"alice@example.com ",                // 带尾随空格，非纯地址
		"昵称 <alice@example.com>",            // 带显示名
		strings.Repeat("a", 130) + "@b.com", // 超长
	}
	for _, s := range invalid {
		if IsValidEmail(s) {
			t.Errorf("IsValidEmail(%q) = true, 期望 false", s)
		}
	}
}
