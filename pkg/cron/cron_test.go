package cron

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// 非法表达式必须被拒绝：是否继续启动由调用方决定，包内不替它吞掉错误。
func TestAddRejectsInvalidSpec(t *testing.T) {
	s := New(nil)
	if err := s.Add("bad", "not-a-cron-spec", func(context.Context) {}); err == nil {
		t.Fatal("非法 cron 表达式应返回错误")
	}
}

// 五段、六段（秒级）与 @every 写法都应能被解析。
func TestAddAcceptsCommonSpecForms(t *testing.T) {
	s := New(nil)
	noop := func(context.Context) {}
	for _, spec := range []string{"*/1 * * * *", "*/10 * * * * *", "@every 1h"} {
		if err := s.Add("spec-"+spec, spec, noop); err != nil {
			t.Errorf("表达式 %q 应被接受，却报错: %v", spec, err)
		}
	}
}

// nextInterval 取连续两次 Next 的差值，不能受「注册时刻离下一次执行多近」影响。
func TestNextInterval(t *testing.T) {
	parser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)

	cases := []struct {
		spec string
		want time.Duration
	}{
		{"*/10 * * * * *", 10 * time.Second},
		{"*/1 * * * *", time.Minute},
		{"@every 1h", time.Hour},
	}
	for _, tc := range cases {
		schedule, err := parser.Parse(tc.spec)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", tc.spec, err)
		}
		if got := nextInterval(schedule); got != tc.want {
			t.Errorf("%q 的调度间隔应为 %v, 实际 %v", tc.spec, tc.want, got)
		}
	}
}

// WithRunOnStart：Start 后任务应立刻先跑一轮，而不必等到表达式命中。
//
// 表达式取「每年 1 月 1 日 0 点」——若没有 RunOnStart，这条断言在测试时限内不可能通过。
func TestRunOnStartFiresImmediately(t *testing.T) {
	s := New(nil)
	ran := make(chan struct{}, 1)
	if err := s.Add("boot", "0 0 1 1 *", func(context.Context) {
		select {
		case ran <- struct{}{}:
		default:
		}
	}, WithRunOnStart()); err != nil {
		t.Fatalf("注册任务失败: %v", err)
	}

	s.Start(context.Background())
	defer s.Stop()

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("声明 RunOnStart 的任务未在启动后立即执行")
	}
}

// 未声明 WithRunOnStart 的任务不应在启动瞬间执行。
func TestWithoutRunOnStartDoesNotFireImmediately(t *testing.T) {
	s := New(nil)
	ran := make(chan struct{}, 1)
	if err := s.Add("idle", "0 0 1 1 *", func(context.Context) {
		select {
		case ran <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatalf("注册任务失败: %v", err)
	}

	s.Start(context.Background())
	defer s.Stop()

	select {
	case <-ran:
		t.Fatal("未声明 RunOnStart 的任务不应在启动后立即执行")
	case <-time.After(200 * time.Millisecond):
	}
}
