# pkg · 可复用公共库

放**与业务无关**、可被任意服务复用的基础能力。这里不允许出现任何业务概念。

| 包 | 职责 |
| --- | --- |
| `config/` | 配置加载（Viper），支持多环境 |
| `logger/` | 日志封装（Zap），统一字段与级别 |
| `database/` | MySQL 连接池与 GORM 初始化 |
| `cache/` | Redis 客户端与缓存工具（Cache Aside） |
| `storage/` | MinIO 对象存储客户端 |
| `mailer/` | 邮件发送：`Sender` 接口 + `New` 工厂，两个驱动 —— `log`（把正文写进日志，供本地取码，仅 dev 允许）与 `smtp`（真实投递：隐式 TLS / STARTTLS、RFC 2047 主题编码、base64 正文、投递总超时；`tls=none` 仅限本机） |
| `mq/` | Kafka 生产者与消费者封装 |
| `registry/` | Etcd 服务注册与发现 |
| `jwt/` | 双 Token 签发与校验 |
| `middleware/` | 通用 Gin 中间件（TraceID、异常恢复、鉴权） |
| `response/` | 统一返回格式 |
| `errcode/` | 业务错误码定义 |
| `snowflake/` | 分布式 ID 生成 |
| `lock/` | Redis 分布式互斥锁：`SET NX PX` 抢锁 + Lua 属主校验释放（多副本「单飞」用，如 video 上传清理） |
| `semaphore/` | Redis 计数信号量（带租期）：`TryAcquire` 原子完成「回收过期租约 → 判余量 → 登记」，用于「同时最多 N 个」且占用跨时段的场景（如转码队列配额预约） |
| `singleflight/` | 跨实例单飞：同一份结果被并发重建时只算一次。`Group` 合并同实例内的并发、`Flight` 用 `lock/` 选出唯一 leader 回源，其余实例在 `waitFor` 内有界等待并复用结果；等不到（leader 太慢 / 崩溃 / Redis 故障）就兜底自算，绝不把请求挂住（缓存击穿时的惊群治理） |
| `cron/` | 周期任务调度：封装 robfig/cron（秒级表达式，五 / 六段写法兼容），可选单飞锁（复用 `lock/`）、panic 兜底与注册日志 |
| `ratelimit/` | 限流（Redis 令牌桶） |
| `circuitbreaker/` | 跨服务调用熔断（三态：闭合 / 打开 / 半开） |
| `metrics/` | Prometheus 指标埋点（HTTP / gRPC 进出站 / 限流 / 熔断）与 pprof 性能剖析（`/debug/pprof`） |
| `tracing/` | 链路追踪：OpenTelemetry 上报 Jaeger（`Setup`）+ TraceID 透传（gRPC metadata）与日志串联 |
| `utils/` | 通用工具函数 |

## 约定

- 不依赖 `internal/`，依赖方向只能是 `internal → pkg`。
- 每个包只暴露必要的最小 API。
