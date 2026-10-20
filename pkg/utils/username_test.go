package utils

import (
	"strings"
	"testing"
)

func TestDeriveUsername(t *testing.T) {
	cases := map[string]string{
		"alice@example.com":       "alice",
		"Alice.Smith@example.com": "alicesmith",
		"a.b-c@example.com":       "abc",
		"user_01@example.com":     "user_01",
		"alice":                   "alice", // 没有 @ 时整个串当本地部分
	}

	for in, want := range cases {
		if got := DeriveUsername(in); got != want {
			t.Errorf("DeriveUsername(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestDeriveUsernameFallback(t *testing.T) {
	// 清洗后不足 3 字符或全部被剔除时，走 cy + 6 位数字兜底。
	for _, in := range []string{
		"a@example.com",
		"ab@example.com",
		"@@@",
		"中文用户@example.com",
		"a.b@example.com",
	} {
		got := DeriveUsername(in)
		if !strings.HasPrefix(got, "cy") || len(got) != len("cy")+6 {
			t.Errorf("DeriveUsername(%q) = %q, 期望 cy + 6 位数字", in, got)
		}
	}
}

func TestDeriveUsernameTruncate(t *testing.T) {
	in := strings.Repeat("a", 40) + "@example.com"
	got := DeriveUsername(in)
	if len(got) != derivedUsernameMaxLen {
		t.Errorf("DeriveUsername(超长) 长度 = %d, 期望 %d", len(got), derivedUsernameMaxLen)
	}
}

func TestSuffixUsername(t *testing.T) {
	got := SuffixUsername("alice")
	if !strings.HasPrefix(got, "alice_") {
		t.Errorf("SuffixUsername(\"alice\") = %q, 期望以 alice_ 开头", got)
	}
	if len(got) != len("alice_")+usernameSuffixLen {
		t.Errorf("SuffixUsername(\"alice\") = %q, 长度 = %d, 期望 %d",
			got, len(got), len("alice_")+usernameSuffixLen)
	}

	// 超长 base 应被截断，保证结果不超过用户名列宽。
	long := SuffixUsername(strings.Repeat("a", 40))
	if len(long) > derivedUsernameMaxLen {
		t.Errorf("SuffixUsername(超长) 长度 = %d, 超过 %d", len(long), derivedUsernameMaxLen)
	}
}
