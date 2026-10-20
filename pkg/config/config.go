// Package config 负责加载与解析应用配置。
//
// 设计要点：
//  1. 使用 Viper 读取 YAML 配置文件，字段与 configs/config.example.yaml 一一对应；
//  2. 支持环境变量覆盖，命名规则为 CHUANGYUE_ + 配置路径（如 mysql.host -> CHUANGYUE_MYSQL_HOST）；
//  3. 环境变量优先级高于配置文件，方便在容器 / CI 中注入密码而不落盘。
package config

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 是全量配置的根结构，通过 mapstructure tag 与 YAML 字段对应。
type Config struct {
	App   AppConfig   `mapstructure:"app"`
	Log   LogConfig   `mapstructure:"log"`
	MySQL MySQLConfig `mapstructure:"mysql"`
	// InteractionMySQL 是互动域的独立库连接参数（物理拆库）。
	// Host 留空时回退为主库 MySQL 配置，让「单库开发」也能直接跑；生产按独立库 + 独立账号配置。
	InteractionMySQL MySQLConfig     `mapstructure:"interaction_mysql"`
	Redis            RedisConfig     `mapstructure:"redis"`
	JWT              JWTConfig       `mapstructure:"jwt"`
	Upload           UploadConfig    `mapstructure:"upload"`
	Mail             MailConfig      `mapstructure:"mail"`
	Verify           VerifyConfig    `mapstructure:"verify"`
	Kafka            KafkaConfig     `mapstructure:"kafka"`
	Transcode        TranscodeConfig `mapstructure:"transcode"`
	Etcd             EtcdConfig      `mapstructure:"etcd"`
	RateLimit        RateLimitConfig `mapstructure:"ratelimit"`
	CircuitBreaker   BreakerConfig   `mapstructure:"circuitbreaker"`
	Tracing          TracingConfig   `mapstructure:"tracing"`
	Jobs             JobsConfig      `mapstructure:"jobs"`
	Search           SearchConfig    `mapstructure:"search"`
}

// SearchConfig 描述检索域的存储后端。
//
// 检索有两种工作方式，由 es.enabled 切换：
//   - 关闭（默认）：沿用视频服务的库内 LIKE 检索，部署最轻；
//   - 打开：查询独立 ES 索引，支持相关度排序与中文短语匹配；
//     ES 不可用时自动退回库内检索，可用性不押在新引入的组件上。
type SearchConfig struct {
	ES ESConfig `mapstructure:"es"`
}

// ESConfig 描述 Elasticsearch 连接参数。
//
// 认证只在自建且开启安全插件时需要；本地 compose / k8s infra 的清单默认关闭安全插件，
// 因此 username / password 留空即可。地址只取第一个：当前是单节点部署，
// 多节点时应交给前置负载均衡，而不是在客户端里做节点轮询。
type ESConfig struct {
	Enabled   bool          `mapstructure:"enabled"`
	Addresses []string      `mapstructure:"addresses"`
	Username  string        `mapstructure:"username"`
	Password  string        `mapstructure:"password"`
	Index     string        `mapstructure:"index"`
	Timeout   time.Duration `mapstructure:"timeout"`
}

// JobsConfig 描述内置周期任务的开关与执行周期。
//
// 表达式支持秒级字段（六段：秒 分 时 日 月 周），也兼容五段写法；周期调整只改配置。
// 各服务只读取自己用到的那几项——feed 用 RankRefresh，video 用 ViewCountFlush 与 UploadCleanup。
//
// 为什么把开关显式化：这些任务会在后台写库 / 写缓存，属于「开了才会发生」的副作用。
// 缺省关闭，由部署方按需打开，避免本地开发时被意外的后台写打扰。
type JobsConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// RankRefresh 是首页热度榜（未登录推荐流）预热周期。
	RankRefresh string `mapstructure:"rank_refresh"`
	// ViewCountFlush 是播放量增量从 Redis 缓冲批量落库的周期。
	ViewCountFlush string `mapstructure:"view_count_flush"`
	// UploadCleanup 是过期上传会话（分片临时文件）回收周期。
	UploadCleanup string `mapstructure:"upload_cleanup"`
}

// AppConfig 描述应用自身的运行参数。
type AppConfig struct {
	Name string `mapstructure:"name"`
	Env  string `mapstructure:"env"`  // dev / test / prod
	Addr string `mapstructure:"addr"` // 监听地址，例如 ":8080"
	Mode string `mapstructure:"mode"` // debug / release
	// TrustedProxies 是可信反向代理的 IP / CIDR 列表，决定 c.ClientIP() 如何取值。
	//
	// 留空 = 不信任任何代理：ClientIP 取直连地址。此时若前面还有 Nginx，
	// 所有请求会被算成同一个（Nginx 的）IP，且客户端伪造的 X-Forwarded-For 会被忽略——
	// 安全但会把按 IP 的限流收成一个桶。
	// 网关前面有 Nginx 时必须填它的地址（K8s 里通常是 Pod/Service CIDR，如 10.42.0.0/16），
	// 这样 gin 才会从 X-Forwarded-For 的**最右侧**取到真实客户端 IP（见 pkg/middleware 的说明）。
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

// LogConfig 描述日志输出行为。
type LogConfig struct {
	Level  string `mapstructure:"level"`  // debug / info / warn / error
	Format string `mapstructure:"format"` // console / json
	Output string `mapstructure:"output"` // stdout 或文件路径
}

// MySQLConfig 描述 MySQL 连接与连接池参数。
type MySQLConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	Username        string        `mapstructure:"username"`
	Password        string        `mapstructure:"password"`
	Database        string        `mapstructure:"database"`
	Charset         string        `mapstructure:"charset"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	LogLevel        string        `mapstructure:"log_level"` // silent / error / warn / info
}

// DSN 拼装 go-sql-driver/mysql 所需的连接串。
// parseTime=True 让驱动把 DATETIME 解析为 time.Time；loc=Local 保证时区一致。
func (m MySQLConfig) DSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local",
		m.Username, m.Password, m.Host, m.Port, m.Database, m.Charset,
	)
}

// RedisConfig 描述 Redis 连接参数。
type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	PoolSize int    `mapstructure:"pool_size"`
}

// Addr 返回 host:port 形式的地址。
func (r RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

// JWTConfig 描述双 Token 的密钥与有效期。
type JWTConfig struct {
	AccessSecret  string        `mapstructure:"access_secret"`
	RefreshSecret string        `mapstructure:"refresh_secret"`
	AccessExpire  time.Duration `mapstructure:"access_expire"`
	RefreshExpire time.Duration `mapstructure:"refresh_expire"`
	Issuer        string        `mapstructure:"issuer"`
}

// DriverMinIOName 是对象存储驱动的配置取值，与 pkg/storage 的常量保持一致；
// 这里独立定义是为了避免 pkg/config 反向依赖 pkg/storage。
const DriverMinIOName = "minio"

// UploadConfig 描述文件上传策略：本地磁盘驱动或对象存储驱动。
type UploadConfig struct {
	Driver     string   `mapstructure:"driver"` // local / minio
	LocalDir   string   `mapstructure:"local_dir"`
	MaxSizeMB  int      `mapstructure:"max_size_mb"`
	AllowedExt []string `mapstructure:"allowed_ext"`
	// ChunkSizeMB 是分片上传的单片大小（MB）；小于等于 0 时由服务层按默认值兜底。
	ChunkSizeMB int `mapstructure:"chunk_size_mb"`
	// MinIO 仅在 Driver=minio 时生效。
	MinIO MinIOConfig `mapstructure:"minio"`
}

// MinIOConfig 描述对象存储驱动的连接参数。
type MinIOConfig struct {
	Endpoint  string `mapstructure:"endpoint"` // 形如 127.0.0.1:9000
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	Bucket    string `mapstructure:"bucket"`
	UseSSL    bool   `mapstructure:"use_ssl"`
	Region    string `mapstructure:"region"`
	// PublicURL 是对象的对外访问前缀（如 CDN 或反向代理地址）；
	// 留空时按 scheme://endpoint/bucket 推导。
	PublicURL string `mapstructure:"public_url"`
}

// MailConfig 描述邮件发送能力。
//
// 两个驱动：
//   - log：把邮件内容写进日志，不真实投递，**仅 dev 环境可用**（生产误配会被启动检查拒绝）。
//   - smtp：真实投递。覆盖 QQ / 163 邮箱、阿里云邮件推送（DirectMail）等所有提供
//     SMTP 服务的厂商——DirectMail 的 SMTP 入口只是换个 host 与账号，无需另写驱动。
type MailConfig struct {
	Driver string `mapstructure:"driver"` // log / smtp
	From   string `mapstructure:"from"`   // 发件人展示名与地址，如 `窗月 <no-reply@x.com>`

	// 以下仅 smtp 驱动使用。Host 留空即视为未配置 SMTP，启动时会被拒绝。
	Host     string `mapstructure:"host"`     // SMTP 服务器地址
	Port     int    `mapstructure:"port"`     // 端口；留空按 tls 模式取默认（ssl→465，starttls→587）
	Username string `mapstructure:"username"` // 登录账号，通常就是发件邮箱
	Password string `mapstructure:"password"` // 授权码 / 客户端专用密码，不是邮箱登录密码
	// TLS 模式：ssl（隐式 TLS，连上即握手）/ starttls（先明文再升级）/ none（不加密，仅限本机）。
	// 留空按端口推断：465 → ssl，其余 → starttls。见 pkg/mailer 的说明。
	TLS string `mapstructure:"tls"`
	// Timeout 是单次投递（连接 + 握手 + 认证 + 发送）的总上限，防止 SMTP 服务器无响应时
	// 把发验证码的请求一直挂住。留空取 mailer 的默认值。
	Timeout time.Duration `mapstructure:"timeout"`
}

// VerifyConfig 描述邮箱验证码策略。
type VerifyConfig struct {
	CodeTTL          time.Duration `mapstructure:"code_ttl"`           // 验证码有效期
	CodeCooldown     time.Duration `mapstructure:"code_cooldown"`      // 同一邮箱两次发码的最小间隔
	CodeMaxTry       int           `mapstructure:"code_max_try"`       // 单个验证码允许的错误次数
	EmailHourlyLimit int           `mapstructure:"email_hourly_limit"` // 单邮箱每小时发码上限
	IPHourlyLimit    int           `mapstructure:"ip_hourly_limit"`    // 单 IP 每小时发码上限
}

// KafkaConfig 描述消息队列的连接与主题。
//
// 承载两类消息：转码任务（Topic/Group）与互动计数回写（StatTopic/StatGroup）。
// 同一消费组内的多个副本会分摊分区，不会重复消费同一条消息。
type KafkaConfig struct {
	Brokers []string `mapstructure:"brokers"`
	Topic   string   `mapstructure:"topic"`
	Group   string   `mapstructure:"group"`
	// StatTopic / StatGroup 用于「互动计数回写视频库只读副本」：互动域生产、视频域消费。
	StatTopic string `mapstructure:"stat_topic"`
	StatGroup string `mapstructure:"stat_group"`
	// DeleteTopic / DeleteGroup 用于「视频删除后清理互动域关联数据」：视频域生产、互动域消费。
	// 之所以走事件而不是让视频域直接调互动域：两个域本来就互相依赖（互动取视频信息），
	// 再加一条反向的同步调用会形成环；事件是单向的，且消费侧天然可幂等重放。
	DeleteTopic string `mapstructure:"delete_topic"`
	DeleteGroup string `mapstructure:"delete_group"`
	// IndexTopic / IndexGroup 用于「视频变更同步到检索索引」：视频域生产、搜索域消费。
	// 走事件而不是让视频域同步写 ES：索引是派生数据，写索引失败不该让上架 / 编辑失败；
	// 消费侧按 video_id 覆盖写，天然幂等。
	IndexTopic string `mapstructure:"index_topic"`
	IndexGroup string `mapstructure:"index_group"`
}

// RenditionConfig 描述一个 HLS 清晰度档位。
type RenditionConfig struct {
	Name    string `mapstructure:"name"`    // 档位名，同时作为输出子目录名
	Height  int    `mapstructure:"height"`  // 目标高度（宽度按源比例自动对齐偶数）
	Bitrate string `mapstructure:"bitrate"` // 视频码率，如 1400k
}

// TranscodeConfig 描述异步转码流水线的参数。
type TranscodeConfig struct {
	// FFmpegPath / FFprobePath 为可执行文件路径，配置为裸名时从 PATH 查找。
	FFmpegPath  string `mapstructure:"ffmpeg_path"`
	FFprobePath string `mapstructure:"ffprobe_path"`
	// OutputDir 是 HLS 产物的根目录，其下按视频 ID 分目录。
	OutputDir string `mapstructure:"output_dir"`
	// SegmentSec 是单个分片时长（秒）。
	SegmentSec int `mapstructure:"segment_sec"`
	// MaxRetry 是单条任务的最大重试次数，耗尽后标记为转码失败。
	MaxRetry   int               `mapstructure:"max_retry"`
	Preset     string            `mapstructure:"preset"`
	Renditions []RenditionConfig `mapstructure:"renditions"`
	// MaxPending 是允许同时「转码中」的视频数上限（背压）。
	//
	// 转码是 CPU 密集型：Worker 串行消费，任务堆积只会让后来者等待更久，
	// 所以在上传入口就把「队列已满」挡掉（返回 503），而不是先收下再排队。
	//
	// 判定口径是「库里已在转码的记录数 + 在途上传的预约数」——后者由 Redis 计数信号量
	// 记账（pkg/semaphore），因为从放行到登记之间隔着整段落盘 / 合并耗时，
	// 只看库里的数会让并发收尾的上传重复取走同一份余量（见 service.reserveTranscodeSlot）。
	// <= 0 表示不限制。
	MaxPending int `mapstructure:"max_pending"`
}

// EtcdConfig 描述服务注册中心 Etcd 的连接参数。
//
// 各微服务启动时把自身地址注册到 Etcd，调用方按服务名发现实例；
// Endpoints 为宿主机可达的客户端地址，对应 compose 里的 etcd 服务。
type EtcdConfig struct {
	Endpoints []string `mapstructure:"endpoints"`
}

// RateLimitConfig 描述网关的限流参数（令牌桶，按 IP 与登录用户两个维度计数）。
//
// 为什么放在网关：网关是唯一对外入口，也是所有请求的必经之路；
// 在读接口（尤其详情页会写播放量）上挡住脚本刷量，成本最低、收益最大。
type RateLimitConfig struct {
	// Enabled 为 true 时才启用；Redis 不可用时一律放行（fail-open）。
	Enabled bool `mapstructure:"enabled"`
	// IPPerSecond / IPBurst：单个来源 IP 的令牌补充速率（个/秒）与桶容量（突发额度）。
	IPPerSecond float64 `mapstructure:"ip_per_second"`
	IPBurst     int     `mapstructure:"ip_burst"`
	// UserPerSecond / UserBurst：单个登录用户的额度，比 IP 更严（同一 NAT 下有多个用户）。
	UserPerSecond float64 `mapstructure:"user_per_second"`
	UserBurst     int     `mapstructure:"user_burst"`
}

// BreakerConfig 描述跨服务调用的熔断器参数。
//
// 熔断器解决的是「下游持续失败时别再等了」：连续失败到阈值就打开，直接快速失败，
// 避免每个请求都白等一次超时。它只统计**可用性类**错误（Unavailable / DeadlineExceeded /
// ResourceExhausted），业务错误（如 404、队列繁忙）视为下游健康，不计入。
type BreakerConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// FailureThreshold 是连续失败达到该次数即打开熔断。
	FailureThreshold int `mapstructure:"failure_threshold"`
	// OpenTimeout 是打开后等待多久进入半开（放少量探测请求）。
	OpenTimeout time.Duration `mapstructure:"open_timeout"`
	// HalfOpenMax 是半开状态下允许的并发探测请求数。
	HalfOpenMax int `mapstructure:"half_open_max"`
}

// TracingConfig 描述链路追踪（OpenTelemetry + Jaeger）参数。
//
// 未配置 endpoint 即视为禁用：各服务不产生 span，日志链路 ID 照旧工作。
type TracingConfig struct {
	// Endpoint 是 OTLP gRPC 接收地址（如 jaeger:4317）；留空表示禁用追踪。
	Endpoint string `mapstructure:"endpoint"`
	// SampleRatio 是根 span 采样率（0~1）：1 表示全采。父子链路沿用父决策。
	SampleRatio float64 `mapstructure:"sample_ratio"`
}

// Load 从指定路径加载配置。
//
// path 为空时按默认顺序查找：./configs/dev/config.yaml -> ./configs/config.yaml。
// 返回的 Config 已完成环境变量覆盖。
func Load(path string) (*Config, error) {
	v := viper.New()

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("./configs/dev")
		v.AddConfigPath("./configs")
	}

	v.SetEnvPrefix("CHUANGYUE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// Viper 的 AutomaticEnv 不会作用于 Unmarshal（它只走 AllSettings），
	// 因此这里显式地为每个叶子字段绑定环境变量，确保覆盖生效。
	bindEnv(v, "", reflect.TypeOf(Config{}))

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	cfg.Normalize()
	return &cfg, nil
}

// Normalize 为可选的 Kafka / 转码配置补默认值，避免下游到处判空。
//
// 这些默认值同时写在 config.example.yaml 里；这里再兜一层，
// 是为了让「配置里没写」的部署也能直接跑起来。
func (c *Config) Normalize() {
	u := &c.Upload
	if u.MinIO.Bucket == "" {
		u.MinIO.Bucket = "chuangyue"
	}
	if u.MinIO.Region == "" {
		u.MinIO.Region = "us-east-1"
	}
	// S3 分片下限 5MiB：minio 驱动下把过小的分片提升到 5MiB，避免运行期才报错。
	// Endpoint 不兜底：未配置时由 storage.New 给出明确错误。
	if strings.EqualFold(strings.TrimSpace(u.Driver), DriverMinIOName) && u.ChunkSizeMB < 5 {
		u.ChunkSizeMB = 5
	}

	if len(c.Kafka.Brokers) == 0 {
		c.Kafka.Brokers = []string{"127.0.0.1:9092"}
	}
	if c.Kafka.Topic == "" {
		c.Kafka.Topic = "chuangyue.transcode.task"
	}
	if c.Kafka.Group == "" {
		c.Kafka.Group = "chuangyue-transcode-worker"
	}
	if c.Kafka.StatTopic == "" {
		c.Kafka.StatTopic = "chuangyue.video.stat"
	}
	if c.Kafka.StatGroup == "" {
		c.Kafka.StatGroup = "chuangyue-video-stat-sync"
	}
	if c.Kafka.IndexTopic == "" {
		c.Kafka.IndexTopic = "chuangyue.video.index"
	}
	if c.Kafka.IndexGroup == "" {
		c.Kafka.IndexGroup = "chuangyue-search-indexer"
	}

	// 检索：ES 地址与索引名有默认值，但 enabled 不兜底——是否启用由部署方显式决定，
	// 避免「没写 search 段」的部署在启动时意外连一个不存在的 ES。
	es := &c.Search.ES
	if len(es.Addresses) == 0 {
		es.Addresses = []string{"http://127.0.0.1:9200"}
	}
	if es.Index == "" {
		es.Index = "chuangyue_videos"
	}
	if es.Timeout <= 0 {
		es.Timeout = 3 * time.Second
	}

	// 互动域未单独配置库时回退主库：单库开发（含单元测试）无需额外配置即可运行。
	if c.InteractionMySQL.Host == "" {
		c.InteractionMySQL = c.MySQL
	}

	// 限流 / 熔断只在「显式开启」时生效（Enabled 是普通 bool，缺省即关）；
	// 这里只补数值默认值，避免开启后因为漏写参数而变成 0 额度（一律拒绝）。
	rl := &c.RateLimit
	if rl.IPPerSecond <= 0 {
		rl.IPPerSecond = 20
	}
	if rl.IPBurst <= 0 {
		rl.IPBurst = 40
	}
	if rl.UserPerSecond <= 0 {
		rl.UserPerSecond = 10
	}
	if rl.UserBurst <= 0 {
		rl.UserBurst = 20
	}

	cb := &c.CircuitBreaker
	if cb.FailureThreshold <= 0 {
		cb.FailureThreshold = 5
	}
	if cb.OpenTimeout <= 0 {
		cb.OpenTimeout = 10 * time.Second
	}
	if cb.HalfOpenMax <= 0 {
		cb.HalfOpenMax = 1
	}

	// 追踪：endpoint 为空即禁用（不兜底，避免误开后向不存在的 collector 刷错误日志）；
	// 配了 endpoint 却没写采样率时按全采兜底，否则会「开了追踪却一条都看不到」。
	if c.Tracing.Endpoint != "" && c.Tracing.SampleRatio <= 0 {
		c.Tracing.SampleRatio = 1
	}

	if len(c.Etcd.Endpoints) == 0 {
		c.Etcd.Endpoints = []string{"127.0.0.1:2379"}
	}

	// 周期任务只补表达式默认值，不碰 Enabled——是否开启由部署方显式决定，
	// 兜底成「默认开启」会让「没写 jobs 段的部署」意外产生后台写。
	j := &c.Jobs
	if j.RankRefresh == "" {
		// 与未登录首页缓存的 TTL（1min）对齐：缓存刚过期就被下一轮预热填上，读请求恒命中。
		j.RankRefresh = "*/1 * * * *"
	}
	if j.ViewCountFlush == "" {
		// 10s 一批：粒度足够细（用户感知不到计数延迟），又足以把大量单行 UPDATE 合并成一批。
		j.ViewCountFlush = "*/10 * * * * *"
	}
	if j.UploadCleanup == "" {
		// 每小时一轮：过期会话多滞留一小时无碍，重点是别让它长期堆积。
		j.UploadCleanup = "0 * * * *"
	}

	t := &c.Transcode
	if t.FFmpegPath == "" {
		t.FFmpegPath = "ffmpeg"
	}
	if t.FFprobePath == "" {
		t.FFprobePath = "ffprobe"
	}
	if t.OutputDir == "" {
		// HLS 产物默认与上传源文件同处一个静态目录之下，由网关统一托管
		t.OutputDir = filepath.Join(c.Upload.LocalDir, "hls")
	}
	if t.SegmentSec <= 0 {
		t.SegmentSec = 6
	}
	if t.MaxRetry <= 0 {
		t.MaxRetry = 2
	}
	if t.MaxPending <= 0 {
		// 默认 10：单 Worker 串行消费下，超过这个量级基本只是在排队等 CPU。
		t.MaxPending = 10
	}
	if t.Preset == "" {
		t.Preset = "veryfast"
	}
	if len(t.Renditions) == 0 {
		t.Renditions = []RenditionConfig{
			{Name: "480p", Height: 480, Bitrate: "1400k"},
			{Name: "720p", Height: 720, Bitrate: "2800k"},
			{Name: "1080p", Height: 1080, Bitrate: "5000k"},
			{Name: "2160p", Height: 2160, Bitrate: "16000k"},
		}
	}
}

// bindEnv 递归遍历配置结构体，为每个叶子字段调用 BindEnv。
// 嵌套结构体（如 mysql）会拼成 "mysql.host" 这样的点号路径。
func bindEnv(v *viper.Viper, prefix string, t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := strings.Split(field.Tag.Get("mapstructure"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}

		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}

		// 仅嵌套结构体需要继续下钻；time.Duration 底层是 int64，属于叶子字段。
		if field.Type.Kind() == reflect.Struct {
			bindEnv(v, key, field.Type)
			continue
		}
		_ = v.BindEnv(key)
	}
}
