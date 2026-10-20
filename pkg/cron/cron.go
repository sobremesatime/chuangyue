// Package cron 提供周期任务的统一调度封装。
//
// 为什么需要它：项目里有若干「每隔一段时间做一件事」的需求（首页热度榜预热、播放量
// 增量批量落库、过期上传清理）。若各自用 `time.Ticker` 自行实现，会同时带来三个问题：
// 多副本部署时每个实例都跑一遍（重复执行）、周期只能写死在代码里、任务内 panic 会带崩进程。
//
// 本包在通用调度器（robfig/cron）之上只补三件事，不做更多：
//  1. 支持秒级 cron 表达式（六段写 `*/10 * * * * *` 也兼容五段写法），周期调整改配置即可；
//  2. 可选的分布式单飞锁（pkg/lock）：同一轮只允许一个实例执行；
//  3. 任务 panic 兜底 + 统一日志。
//
// 与 pkg/lock 一致的取向：本包只提供能力，不做 fail-open / fail-closed 的判断。
// 需要单飞就传 WithLock；不需要（任务天然幂等或自带单飞）就别传。
package cron

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/lock"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
)

const (
	// lockKeyPrefix 是定时任务单飞锁的键前缀，后面接任务名（如 lock:cron:video-upload-cleanup）。
	lockKeyPrefix = "lock:cron:"
	// stopTimeout 是 Stop 等待在途任务收尾的上限，避免个别任务卡住拖住进程退出。
	stopTimeout = 5 * time.Second
)

// Scheduler 是带单飞与 panic 兜底的周期任务调度器。
//
// 一个进程通常只需要一个：把进程内所有周期任务注册到同一个调度器，便于统一启动与收尾。
type Scheduler struct {
	c    *cron.Cron
	rdb  *redis.Client
	ctx  context.Context
	boot []bootJob // 声明了「启动即先跑一轮」的任务
}

// bootJob 是需要启动即先跑一次的任务。
type bootJob struct {
	name string
	fn   func(context.Context)
	lk   *lock.Mutex
}

// JobOption 是注册任务时的可选项。
type JobOption func(*jobConfig)

type jobConfig struct {
	lockTTL    time.Duration
	runOnStart bool
}

// WithLock 开启单飞：多副本部署时同一轮只允许一个实例执行。
//
// ttl 是锁租期，取值规则与 pkg/lock 一致——**大于单轮任务的最长耗时**（正常跑完不被插队），
// 同时**小于执行间隔**（持有者崩溃后最多跨过一轮就能自愈）。
func WithLock(ttl time.Duration) JobOption {
	return func(o *jobConfig) { o.lockTTL = ttl }
}

// WithRunOnStart 让任务在 Start 后立刻先跑一轮，之后才按表达式周期执行。
//
// 适用于「回收上一次进程遗留状态」这类任务：若等到下一个周期边界才首次执行，
// 遗留物会多滞留一整个周期。
func WithRunOnStart() JobOption {
	return func(o *jobConfig) { o.runOnStart = true }
}

// New 构造调度器。rdb 为 nil 时单飞锁不可用（传了 WithLock 也只会告警并退化为不加锁执行）。
func New(rdb *redis.Client) *Scheduler {
	return &Scheduler{
		c: cron.New(
			// 秒级字段「可选」：五段（分 时 日 月 周）与六段（多一个秒）都能解析——
			// SecondOptional 正是为此而设，于是「每小时」(0 * * * *)与「每 10 秒」
			// (*/10 * * * * *)两种写法可以在同一套配置里共存。
			cron.WithParser(cron.NewParser(
				cron.SecondOptional|cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow|cron.Descriptor,
			)),
			// 用本地时区解析表达式：容器里设了 TZ=Asia/Shanghai，
			// 否则 "0 3 * * *" 会被当成 UTC 三点。
			cron.WithLocation(time.Local),
		),
		rdb: rdb,
	}
}

// Add 注册一个周期任务。name 是任务名（用于日志与单飞锁键），spec 是 cron 表达式。
//
// 表达式非法时返回错误，由调用方决定是告警跳过还是终止启动——本包不替调用方判断。
func (s *Scheduler) Add(name, spec string, fn func(context.Context), opts ...JobOption) error {
	var jc jobConfig
	for _, o := range opts {
		o(&jc)
	}

	var lk *lock.Mutex
	if jc.lockTTL > 0 {
		if s.rdb == nil {
			logger.L().Warn("未接入 Redis，定时任务的单飞锁不可用，退化为多副本各跑一轮",
				zap.String("job", name))
		} else {
			lk = lock.New(s.rdb, lockKeyPrefix+name, jc.lockTTL)
		}
	}

	entryID, err := s.c.AddFunc(spec, func() { s.run(name, fn, lk) })
	if err != nil {
		return fmt.Errorf("注册定时任务 %s（%s）失败: %w", name, spec, err)
	}

	// 记录调度间隔，供「停摆」告警计算阈值。
	//
	// 取「连续两次 Next 的差值」而不是「下一次执行 - 现在」：后者在注册时刻恰好接近
	// 执行边界时会得到一个偏小的值（例如每小时任务在 10:59 注册，差值只有 1 分钟），
	// 告警阈值会被压得过紧。连续两次 Next 对固定周期表达式（含 @every）才是真实间隔。
	entry := s.c.Entry(entryID)
	if interval := nextInterval(entry.Schedule); interval > 0 {
		metrics.Default().RegisterCronJob(name, interval)
	}

	if jc.runOnStart {
		s.boot = append(s.boot, bootJob{name: name, fn: fn, lk: lk})
	}
	// 记一条注册日志：周期任务没有对外接口，出问题时「到底注册了没、周期是多少」
	// 往往只能靠日志回答。
	logger.L().Info("定时任务已注册",
		zap.String("job", name), zap.String("spec", spec),
		zap.Bool("run_on_start", jc.runOnStart), zap.Bool("singleflight", lk != nil))
	return nil
}

// nextInterval 按调度器推算两次连续执行之间的间隔。
func nextInterval(schedule cron.Schedule) time.Duration {
	now := time.Now()
	first := schedule.Next(now)
	return schedule.Next(first).Sub(first)
}

// Start 启动调度；对声明了 WithRunOnStart 的任务，额外立刻先跑一轮。
//
// ctx 是任务执行的上下文，通常传进程的信号上下文（进程退出即取消）。
func (s *Scheduler) Start(ctx context.Context) {
	s.ctx = ctx
	s.c.Start()
	for _, j := range s.boot {
		go s.run(j.name, j.fn, j.lk)
	}
}

// Stop 停止调度并等待在途任务收尾。
//
// 为什么要等：任务可能正处在「已从缓冲区取走增量、尚未落库」的中间态，
// 直接退出会把这一批增量留在半路；等它跑完（有超时兜底）比粗暴退出更划算。
func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	select {
	case <-s.c.Stop().Done():
	case <-time.After(stopTimeout):
		logger.L().Warn("等待定时任务收尾超时，直接退出")
	}
}

// run 是任务的实际执行体：单飞（可选）→ panic 兜底 → 调用任务函数。
func (s *Scheduler) run(name string, fn func(context.Context), lk *lock.Mutex) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if lk != nil {
		ok, err := lk.TryLock(ctx)
		switch {
		case err != nil:
			// 与调用方的既有取向一致（video 上传清理、网关限流）：锁组件故障 fail-open。
			// 重复执行是否安全交由任务自身保证，不该因为锁组件故障就让任务停摆。
			logger.L().Warn("获取定时任务单飞锁失败，本轮不加锁执行", zap.String("job", name), zap.Error(err))
		case !ok:
			logger.L().Debug("定时任务单飞锁被其它实例持有，跳过本轮", zap.String("job", name))
			// skipped 也计入执行次数，便于区分「没跑」与「跑了但失败」；
			// 它不刷新 last_success——本轮确实没有在本实例完成任何工作。
			metrics.Default().ObserveCronRun(name, "skipped")
			return
		default:
			// 释放用 WithoutCancel：进程退出时 ctx 已取消，仍需把锁交还，
			// 否则要等到租期到期，下一个实例才能接手。
			defer func() { _, _ = lk.Unlock(context.WithoutCancel(ctx)) }()
		}
	}

	// 任务 panic 不该带崩整个服务：调度器在 goroutine 里跑任务，
	// 一个 panic 会终止进程，而周期任务属于后台旁路，代价与收益完全不对称。
	defer func() {
		if r := recover(); r != nil {
			metrics.Default().ObserveCronRun(name, "failed")
			logger.L().Error("定时任务 panic，已恢复", zap.String("job", name), zap.Any("panic", r))
		}
	}()

	fn(ctx)
	// 执行完成即视为成功：任务内部的业务失败由任务自己打日志，
	// 这里关心的是「调度器有没有把任务跑起来」——停摆告警依赖这个时间戳。
	metrics.Default().ObserveCronRun(name, "success")
}
