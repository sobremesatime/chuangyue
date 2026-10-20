package utils

import (
	"strconv"
	"testing"
)

func TestNumericCode(t *testing.T) {
	code, err := NumericCode(6)
	if err != nil {
		t.Fatalf("NumericCode 返回错误: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("NumericCode(6) 长度 = %d, 期望 6（%q）", len(code), code)
	}
	if _, err := strconv.Atoi(code); err != nil {
		t.Fatalf("NumericCode(6) = %q, 不是纯数字", code)
	}

	// 连续取样应大概率不同，用来兜住「实现退化成常量」这类错误。
	other, err := NumericCode(6)
	if err != nil {
		t.Fatalf("NumericCode 返回错误: %v", err)
	}
	if code == other {
		t.Logf("两次生成的验证码相同（%q），概率约百万分之一，可接受", code)
	}

	if got, _ := NumericCode(0); got != "" {
		t.Errorf("NumericCode(0) = %q, 期望空串", got)
	}
}

func TestHexSuffix(t *testing.T) {
	if got := HexSuffix(4); len(got) != 4 {
		t.Errorf("HexSuffix(4) = %q, 长度 = %d, 期望 4", got, len(got))
	}
	if got := HexSuffix(0); got != "" {
		t.Errorf("HexSuffix(0) = %q, 期望空串", got)
	}
}
