# 窗月 · 流媒体平台

> 一个「管理员发布内容 + 用户消费内容」的流媒体平台，覆盖视频上传、转码分发、检索与用户互动的完整链路。

窗月（chuangyue）定位为简化版 B站 / YouTube：管理员上传视频，普通用户浏览、播放、搜索，并通过点赞、收藏、评论、弹幕参与互动。产品从单体起步，先把业务与数据模型打磨稳定，再按版本迭代逐步演进为微服务架构，最终补齐分布式与工程化运维能力。

---

## 特性一览

- **内容消费**：视频列表、详情播放（HLS 拖拽与清晰度切换）、分类浏览、标题 / 简介模糊搜索、热门排序
- **用户互动**：点赞、收藏、稍后再看、评论（支持二级回复与评论点赞）、弹幕、观看历史、站内通知
- **内容管理**：管理员单文件上传、异步转码、上架 / 下架、评论与弹幕检索删除、举报处理
- **账号体系**：邮箱验证码注册登录、JWT 双 Token、两级 RBAC 权限（管理员 / 普通用户）、个人资料与头像

**明确不做**：用户上传、UP主体系、投币、直播、付费订阅、版权审核与风控；客户端仅提供 Web 浏览器端，不做原生 App / 小程序。这些取舍让产品聚焦在内容发布与消费的核心链路上。

---

## 技术栈

| 分类 | 技术 |
|---|---|
| 语言 | Go |
| 后端框架 | Gin |
| 前端 | Vue 3 · Vite · TypeScript · Pinia |
| 数据库 | MySQL · GORM |
| 缓存 | Redis |
| 认证鉴权 | JWT（双 Token）· RBAC |
| 服务通信 | gRPC · Protobuf |
| 服务发现 | Etcd |
| 消息队列 | Kafka |
| 对象存储 | MinIO |
| 媒体处理 | FFmpeg · HLS（m3u8 + ts） |
| 分布式 | Snowflake · Redis 分布式锁 / 计数信号量 / 跨实例单飞 · 令牌桶限流 · 熔断降级 |
| 可观测性 | Prometheus · Alertmanager · Grafana · Jaeger · pprof |
| 部署运维 | Docker · Docker Compose · Kubernetes · GitHub Actions |

> 上表是项目的**目标技术栈**，落地分阶段推进：
>
> - **已落地**：Go · Gin · GORM · MySQL · Redis · JWT · Kafka · FFmpeg（HLS 转码）· Vue 3 / Vite / TypeScript / Pinia；**gRPC / Protobuf 与 Etcd**——用户、视频、互动、推荐、搜索五域已是独立进程，经 Etcd 注册发现、以 gRPC 互调，网关退化为「HTTP ↔ gRPC」翻译层；**可观测性**——Prometheus 埋点 + 告警规则 + Alertmanager 告警处置 + Grafana 看板（入口流量、限流 / 熔断保护措施）、`/debug/pprof` 性能剖析、OpenTelemetry 链路追踪上报 Jaeger（网关根 span 串起跨服务调用树）；**CI/CD**——GitHub Actions 跑 vet / build / test、前端构建、Kustomize 清单渲染校验，并在 main 与版本标签上构建推送 8 个镜像（7 服务 + web）到 GHCR。
> - **部分落地**：对象存储抽象已就绪（`pkg/storage` 提供本地磁盘与 MinIO 两种驱动），当前默认本地磁盘并由网关静态托管。
> - **服务层进度**：五个服务域（用户 / 视频 / 互动 / 推荐 / 搜索）均已可用与对外；推荐经 `GET /api/v1/feed`、搜索经 `GET /api/v1/search/videos`。

---

## 架构总览

系统按职责分为六层，其中**可观测层横向贯穿其余各层**。**当前版本已落地**：服务层的用户 / 视频 / 互动 / 推荐 / 搜索五域为独立 gRPC 进程（经 Etcd 注册发现），异步层的「Kafka + 转码 Worker」已就绪，网关退化为「HTTP ↔ gRPC」翻译层；可观测层为后续版本。

```
客户端       Web 浏览器端
              ↓
接入层       Nginx 反向代理 · API 网关（Gin）
              ↓
服务层       用户服务 · 视频服务 · 互动服务 · 推荐服务 · 搜索服务
              ↓
异步层       Kafka 消息队列 · 转码 Worker
              ↓
存储层       MySQL · Redis · Etcd · MinIO 对象存储
              ↓
可观测层     Prometheus + Grafana · Jaeger · pprof
```

**五个微服务职责与落地状态**

| 服务 | 核心职责 | 状态 |
|---|---|---|
| 用户服务 | 注册登录、JWT 双 Token、RBAC 权限、个人资料、观看历史 | 已落地（`cmd/user`） |
| 视频服务 | 分片上传、转码任务投递、视频元数据、分类标签、播放地址下发 | 已落地（`cmd/video`） |
| 互动服务 | 点赞、收藏、评论与回复、弹幕、站内通知、计数 | 已落地（`cmd/interaction`） |
| 推荐服务 | 首页推荐流、热门榜、分类浏览 | 已落地（`cmd/feed` + `GET /api/v1/feed`） |
| 搜索服务 | 视频标题与标签检索 | 已落地（`cmd/search` + `GET /api/v1/search/videos`） |

**核心设计思想**：同步路径只做必要的事，重活全部异步。上传接口落盘后立即返回，转码在后台进行，用户无需等待。

---

## 目录结构

单仓多服务（monorepo）+ 前后端分离：后端按 Go 社区通行的 `cmd` / `internal` / `pkg` 分层，前端独立在 `frontend/`，契约集中在 `api/`，部署产物集中在 `deploy/`。

```
chuangyue/
├── .github/workflows/      # CI/CD 流水线
├── api/                    # 对外契约
│   ├── openapi/            # 前后端 HTTP 契约（OpenAPI 3.0）
│   └── proto/              # 服务间 gRPC 契约（Protobuf）
├── cmd/                    # 各服务入口，只放 main
│   ├── gateway/            # API 网关（唯一 HTTP 入口，翻译为 gRPC）
│   ├── user/               # 用户服务（gRPC :9103）
│   ├── video/              # 视频服务（gRPC :9102）
│   ├── interaction/        # 互动服务（gRPC :9101）
│   ├── feed/               # 推荐服务（gRPC :9104）
│   ├── search/             # 搜索服务（gRPC :9105）
│   └── transcode-worker/   # 转码 Worker（消费 Kafka 任务）
├── internal/               # 私有业务实现，按业务域划分
│   ├── gateway/            # 网关：handler（HTTP↔gRPC 翻译）+ router（依赖注入）
│   ├── user/               # 账号、资料、邮箱验证码
│   ├── video/              # 视频、上传、举报、转码状态
│   ├── interaction/        # 评论、弹幕、点赞/收藏/历史、站内通知
│   ├── feed/               # 首页推荐聚合（读视频 + 互动信号重排）
│   ├── search/             # 视频检索（经视频服务检索，不持有业务表）
│   ├── transcode/          # FFmpeg 多码率 HLS 转码与任务消费
│   └── <domain>/{handler, service, repository, model, dto}
├── pkg/                    # 可复用公共库
│   └── config/ logger/ database/ cache/ mq/ mailer/ registry/
│       grpcclient/ jwt/ middleware/ response/ errcode/ snowflake/ utils/
├── frontend/               # 前端（Vue 3 + Vite + TypeScript）
│   └── src/{api, components, composables, layouts, router,
│            stores, styles, types, utils, views}
├── configs/                # 配置样例与多环境配置
├── migrations/             # 数据库迁移 SQL
├── deploy/                 # compose/ docker/ k8s/ monitoring/ nginx/ scripts/
├── scripts/                # 开发辅助脚本
├── test/                   # 集成测试与压测
├── Makefile
└── go.mod
```

**前后端分离**：后端不渲染页面，只提供 `/api/v1/*` 的 JSON 接口；前端独立构建，产物由 Nginx 托管。开发环境用 Vite proxy 把 `/api` 转发到网关，生产环境由 Nginx 同域分发，天然无跨域。详细约定见设计文档 11.1 节。

---

## 快速开始

### 环境要求

- Go 1.26+
- Node.js 20+（前端）
- Docker / Docker Compose
- FFmpeg（转码 Worker 依赖，`ffmpeg` / `ffprobe` 需在 PATH 中）

### 1. 拉起本地依赖

```bash
# 基础依赖：MySQL + Redis + Etcd（服务注册中心，默认拉起）
docker compose -f deploy/compose/docker-compose.yml up -d
# 可选依赖：Kafka（异步转码 / 索引同步）、MinIO（对象存储）、Elasticsearch（全文检索）
# 都挂在 full profile 下；不起 ES 时搜索会自动退回库内 LIKE
docker compose -f deploy/compose/docker-compose.yml --profile full up -d
```

基础 compose 启动 MySQL、Redis 与 **Etcd**；Kafka、MinIO 与 Elasticsearch 挂在 `full` profile 下按需启动，数据通过 volume 持久化，重启不丢数据。
MySQL 首次启动会自动执行 `migrations/` 下的建表脚本，创建用户、视频、互动、评论、弹幕、通知、举报等全部表。

> 用户 / 视频 / 互动三域已拆为独立进程，**Etcd 是运行必需**：各服务启动时把自身注册到 Etcd，网关与其它的服务间调用都按服务名发现、不写死 IP。

**想跳过第 3、4 步？** 也可以用 Compose 一键起全栈（网关 + 五个服务 + 转码 Worker，首次需要 `--build`）：

```bash
docker compose -f deploy/compose/docker-compose.yml --profile app up -d --build
```

详见 [deploy/README.md](deploy/README.md)。

### 2. 准备配置

```bash
cp configs/config.example.yaml configs/dev/config.yaml
```

`configs/dev/config.yaml` 已被 `.gitignore` 忽略，可安全填写本地密码；
也可用环境变量覆盖任意配置项（命名规则见 `.env.example`，如 `CHUANGYUE_MYSQL_PASSWORD`）。

### 3. 启动后端服务

三域已是独立进程，**先起各服务、再起网关**（网关经 Etcd 发现它们）。各服务用独立终端启动：

```bash
go run ./cmd/user        -c configs/dev/config.yaml   # 用户服务  gRPC :9103
go run ./cmd/video       -c configs/dev/config.yaml   # 视频服务  gRPC :9102
go run ./cmd/interaction -c configs/dev/config.yaml   # 互动服务  gRPC :9101
```

按需启动：

```bash
go run ./cmd/feed             -c configs/dev/config.yaml   # 推荐服务 gRPC :9104：首页推荐流 /api/v1/feed 依赖它
go run ./cmd/search           -c configs/dev/config.yaml   # 搜索服务 gRPC :9105：视频检索 /api/v1/search/videos 依赖它
go run ./cmd/transcode-worker -c configs/dev/config.yaml   # 转码 Worker：消费 Kafka 任务，仅上传转码链路需要
```

### 4. 启动网关

```bash
go run ./cmd/gateway -c configs/dev/config.yaml
```

启动后访问 `http://localhost:8080/healthz`，应返回 `{"code":0,"message":"成功",...}`。

### 5. 启动前端

```bash
cd frontend
npm install
npm run gen:types
npm run dev
```

打开 `http://localhost:5173`。开发环境由 Vite 把 `/api`、`/uploads` 代理到网关，
前后端同源，无需处理跨域。

### 6. 初始化管理员账号

注册接口创建的账号固定是普通用户，上传视频需要管理员权限：

1. 在前端注册页创建一个账号（例如 `admin`）；
2. 在数据库里提权：`UPDATE user SET role = 1 WHERE username = 'admin';`，然后重新登录。

详见 [migrations/README.md](migrations/README.md#初始化管理员账号)。

### 当前版本已实现接口

完整契约见 [`api/openapi/chuangyue.v1.yaml`](api/openapi/chuangyue.v1.yaml)（前端据此生成 TS 类型）。按模块归纳：

| 模块 | 方法 | 路径 | 说明 | 权限 |
|---|---|---|---|---|
| 认证 | POST | `/api/v1/auth/email/code` | 发送邮箱验证码（`purpose` = register / reset） | 公开 |
| 认证 | POST | `/api/v1/auth/register` | 邮箱 + 验证码 + 密码注册 | 公开 |
| 认证 | POST | `/api/v1/auth/login` | 登录，返回访问令牌并写刷新令牌 Cookie | 公开 |
| 认证 | POST | `/api/v1/auth/refresh` | 用刷新令牌换取新访问令牌 | 公开 |
| 认证 | POST | `/api/v1/auth/logout` | 退出登录 | 登录 |
| 认证 | POST | `/api/v1/auth/password/reset` | 忘记密码：用邮箱验证码重置（成功后吊销该用户全部刷新令牌） | 公开 |
| 账号 | GET | `/api/v1/users/me` | 当前用户资料 | 登录 |
| 账号 | PATCH | `/api/v1/users/me` | 更新资料（部分更新） | 登录 |
| 账号 | PUT | `/api/v1/users/me/password` | 修改密码 | 登录 |
| 账号 | POST | `/api/v1/users/me/avatar` | 上传 / 更换头像 | 登录 |
| 视频 | GET | `/api/v1/videos` | 视频列表（分页 / 分类 / 关键词 / 排序） | 公开 |
| 视频 | GET | `/api/v1/videos/{id}` | 视频详情 | 公开 |
| 推荐 | GET | `/api/v1/feed` | 首页推荐流（未登录按热度、登录按个性化） | 公开 |
| 搜索 | GET | `/api/v1/search/videos` | 视频检索（关键词 + 分类过滤） | 公开 |
| 互动 | GET | `/api/v1/videos/{id}/interaction` | 查询当前用户点赞 / 收藏态 | 登录 |
| 互动 | POST / DELETE | `/api/v1/videos/{id}/like` | 点赞 / 取消点赞 | 登录 |
| 互动 | POST / DELETE | `/api/v1/videos/{id}/favorite` | 收藏 / 取消收藏 | 登录 |
| 互动 | POST / DELETE | `/api/v1/videos/{id}/watch-later` | 加入 / 移出稍后再看 | 登录 |
| 互动 | POST | `/api/v1/videos/{id}/history` | 上报观看历史 | 登录 |
| 互动 | GET | `/api/v1/users/me/likes`、`favorites`、`watch-later`、`history` | 「我」的列表（分页） | 登录 |
| 评论 | GET / POST | `/api/v1/videos/{id}/comments` | 评论列表 / 发表评论 | 公开 / 登录 |
| 评论 | GET | `/api/v1/comments/{id}/replies` | 一级评论的回复列表 | 公开 |
| 评论 | DELETE | `/api/v1/comments/{id}` | 删除自己的评论 | 登录 |
| 评论 | POST / DELETE | `/api/v1/comments/{id}/like` | 点赞 / 取消点赞评论 | 登录 |
| 弹幕 | GET / POST | `/api/v1/videos/{id}/danmaku` | 拉取 / 发送弹幕 | 公开 / 登录 |
| 弹幕 | DELETE | `/api/v1/danmaku/{id}` | 删除弹幕 | 登录 |
| 通知 | GET | `/api/v1/users/me/notifications` | 通知列表（分页） | 登录 |
| 通知 | GET / POST | `/api/v1/users/me/notifications/unread`、`read` | 未读数 / 标记已读 | 登录 |
| 举报 | POST | `/api/v1/reports` | 发起举报 | 登录 |
| 管理 | POST / GET | `/api/v1/admin/videos` | 上传视频 / 视频列表 | 管理员 |
| 管理 | PATCH / PUT / DELETE | `/api/v1/admin/videos/{id}` | 上下架 / 编辑 / 删除（删除会经事件连带清理该视频的互动数据） | 管理员 |
| 管理 | GET / PATCH | `/api/v1/admin/users`、`/api/v1/admin/users/{id}` | 用户检索（关键词 / 角色 / 状态）/ 封禁解封 / 角色调整 | 管理员 |
| 管理 | GET / DELETE | `/api/v1/admin/comments`、`/api/v1/admin/comments/{id}` | 评论检索 / 删除 | 管理员 |
| 管理 | GET / DELETE | `/api/v1/admin/danmaku`、`/api/v1/admin/danmaku/{id}` | 弹幕检索 / 删除 | 管理员 |
| 管理 | GET / PATCH | `/api/v1/admin/reports`、`/api/v1/admin/reports/{id}` | 举报列表 / 处理 | 管理员 |

---

## 实施路线

按四阶段推进，每个阶段都产出可运行的系统：

| 阶段 | 目标 | 主要内容 | 验收标准 |
|---|---|---|---|
| 一 · 单体起步 | 跑通核心业务闭环 | Gin 脚手架、GORM 建表、Redis 缓存、JWT 登录、管理员上传与视频列表 | 注册登录、上传、播放列表全部可用 |
| 二 · 引入异步 | 打通转码流水线 | MinIO、Kafka、转码 Worker、HLS 切片、状态回写 | 上传后自动转码，转码完成即可播放 |
| 三 · 拆分微服务 | 完成服务化改造 | 用户 / 视频 / 互动 / 推荐 / 搜索五域独立进程、Protobuf 契约、gRPC 与 Etcd、网关改为 HTTP↔gRPC 翻译 | 服务独立部署，调用不写死 IP（五个服务域均已达成） |
| 四 · 生产化收尾 | 补齐分布式与运维能力 | 限流熔断、分布式锁、Prometheus、Jaeger、pprof、K8s 与 CI/CD | 压测有数据、监控可观测、流水线可发布 |

> 进度：阶段一、二已完成；**阶段三已全部落地**（五域独立进程、链路透传、超时、重试、鉴权透传、互动域物理拆库 + 计数归位、Compose 全栈编排、K8s 清单）。
>
> 阶段四（生产化收尾）**已全部落地**：
>
> - ✅ 网关限流（Redis 令牌桶，IP + 用户双维度）
> - ✅ 跨服务熔断（三态熔断器，连续失败快速失败，冷却后半开探测）
> - ✅ 降级（feed 退回兜底快照、search 退回空结果，不再整页 500）
> - ✅ 转码队列背压（超过在转上限时上传前直接拒绝，替代「上传限流」）：判定口径是「库中已在转码数 + 在途上传的预约数」，后者由 `pkg/semaphore`（Redis 计数信号量，带租期）记账——原先只读一次库计数属于检查-使用竞态（检查在落盘前、入队在落盘后，中间隔着整段合并耗时），并发收尾的上传会重复取走同一份余量；现在「判余量 + 占位」在一次 Lua 内原子完成，`Upload` / `CompleteUpload` 全程持有预约、收尾交还，崩溃则由租期兜底回收。实测 16 个并发预约放行数恰好等于余量；**秒传路径也纳入闸门**——`InitUpload` 命中已完成会话时虽不传字节，但它同样要登记视频并投递转码任务（HLS 产物按 `video_id` 分目录，复用源文件也得重新转一遍），因此照样占一个队列槽位，原先这条分支直接收尾、绕开了闸门
> - ✅ 可信反向代理配置（修正 `ClientIP`，堵住伪造 `X-Forwarded-For` 绕过限流）
> - ✅ Prometheus 埋点（网关 `/metrics`；各服务 `:92xx/metrics`，含 HTTP / gRPC 进出站 / 限流拒绝 / 熔断状态）
> - ✅ 监控栈（Prometheus + Grafana，compose `monitoring` profile）：抓取 8 个目标，两块看板——「网关总览」（QPS / P99 / 错误率 / 按路由下钻）与「保护措施与下游健康」（限流拒绝、熔断状态、gRPC 出站失败）
> - ✅ 告警规则与 Alertmanager（`deploy/monitoring`，**阶段四清单外追加**——与周期任务同属设计上声明、此前未落地的能力）：Prometheus 侧 5 组 11 条规则（[alerts.yml](deploy/monitoring/prometheus/alerts.yml)）覆盖**可用性 / 入口 / 依赖 / 运行时 / 后台任务**五类——目标掉线、按路由的 5xx 占比与 P99、限流拒绝占比、熔断器未闭合、出站失败率与 P99、协程数与 RSS、转码最终失败与周期任务停摆；Alertmanager（compose 新增，`http://127.0.0.1:9093`）负责分组 / 抑制 / 路由，本地不外发通知（告警落在 UI）。两处刻意设计：**比例类告警都带最小流量护栏**（低流量路由上「1 个请求里 1 个 5xx」就是 100%，不加护栏必出假警报）；**熔断告警判「未闭合」而非「已打开」**（打开态只维持 10s 冷却窗口，写成等 `==2` 持续 2 分钟这条规则永远哑炮）。抑制规则把「下游掉线」派生出的依赖类告警按 `service` 压掉。**实机验证**：停掉 `video` 容器并以约 3 req/s 持续打详情接口 5 分钟后，Prometheus 侧 `TargetDown` / `CircuitBreakerNotClosed` / `DownstreamCallFailing` / `GatewayHighErrorRate` **4 条同时 firing**，Alertmanager 侧只剩 `TargetDown` 处于 active（`GatewayHighErrorRate` 按 path 维度、无同名标签可对齐，按设计不抑制），另两条 `suppressed` 且 `inhibitedBy` 指向 `TargetDown` 的 fingerprint；`docker start chuangyue-video` 后 `TargetDown` 立即 resolved、抑制随之解除，其余两条在 5 分钟窗口滑出后自行恢复——**能响、也能自己停**。配置分别经 `promtool check rules`（11 rules）与 `amtool check-config` 校验通过。原先**两处「想告警但无指标可依」的缺口已补齐**：转码 Worker 上报 `chuangyue_transcode_jobs_total{result}`（结果**落库成功之后**才计数，避免重投重复计）配 `TranscodeFailed`，「15 分钟内出现最终失败即告警」；`pkg/cron` 上报「最近成功时间戳 + 调度间隔 + 执行结果计数」三组指标配 `CronJobStalled`，判据用**时间戳**而非错误计数（任务可能只是不再被调度 / 卡死 / 进程被杀，都不产生错误）。后台任务不经 HTTP / gRPC，其失败与停摆必须由任务自身上报才看得见
> - ✅ pprof 性能剖析：网关与各服务的 `/metrics` 端口同时暴露 `/debug/pprof`（CPU / 堆 / 协程等），可 `go tool pprof` 在线采样；与 `/metrics` 同属运维接口，挂在业务限流之外，生产须在接入层限制为监控网段可达
> - ✅ 链路追踪（OpenTelemetry → Jaeger）：网关 HTTP 请求为根 span，出站 gRPC 与下游入站各接一层，Jaeger 里可看完整跨服务调用树；采样 `ParentBased(TraceIDRatioBased)` 不断链，日志 `trace_id` 与 Jaeger trace **同值**。`tracing.endpoint` 留空即禁用（不阻断启动）
> - ✅ CI/CD：CI（`.github/workflows/ci.yml`）跑 `go vet` / `go build` / `go test -race`、前端构建，并用 `kubectl kustomize` 离线渲染 dev / prod overlay（零凭据）；镜像发布（`release.yml`）在 main 与 `v*.*.*` 标签上按矩阵构建推送 8 个镜像到 GHCR（`packages: write` + 内置 `GITHUB_TOKEN`，无需额外 Secret），overlays 镜像地址同步指向 `ghcr.io/<owner>/chuangyue/*`
> - ✅ 分布式锁（`pkg/lock`）：`SET key token NX PX ttl` 单命令抢锁（原子 + 租期兜底，持有者崩溃最多占满一个 TTL），释放用 Lua「先比较属主 token 再 DEL」。首个落地场景是 **video 过期上传清理的任务单飞**——多副本同时在线时每轮仅一个实例执行；抢不到跳过本轮、Redis 故障 fail-open（不阻断服务启动）。`go test -tags integration ./pkg/lock/...` 覆盖互斥 / 属主校验 / 租期过期 / 未持锁释放
> - ✅ feed 缓存单飞（`pkg/singleflight`，**阶段四清单外追加**——读路径的惊群治理）：首页缓存未命中时，同一份结果被并发重建的问题分两层解决——`Group` 把**同实例内**同一 key 的并发合并成一次 build；`Flight` 再用 `pkg/lock` 在**跨实例**之间选出唯一 leader 回源，其余实例在 `waitFor`（500ms）内有界轮询复用 leader 写回的结果。刻意取舍是**有界等待 + 兜底自算**：等不到（leader 太慢 / 崩溃 / Redis 故障）就自己算，绝不把请求挂住——可用性不押在 leader 上，代价是 follower 最坏多等一个 `waitFor`。**实机 A/B**（单副本、30 个 VU 对齐同一毫秒打冷缓存）：改造前 video 侧 `ListFeedCandidates` 增量 **30**（一请求一回源）、中位耗时 155ms；改造后增量 **1**、30 个请求平均 15.6ms。`redis-cli MONITOR` 可见 `SET feed:home:lock:* EX 3 NX` 抢锁 → 写缓存 → Lua 属主校验释放的完整时序。pkg 层（跨实例选主 / 等待超时兜底 / 无 Redis 自算 / 锁租期接手）+ feed 层（32 并发只回源一次）集成测试覆盖，见 `go test -tags integration ./pkg/singleflight/... ./internal/feed/...`
> - ✅ 周期任务（`pkg/cron`，**阶段四清单外追加**——属设计上声明、此前未落地的能力）：统一封装 robfig/cron（秒级表达式，五段 / 六段写法兼容），补上项目里缺的三件事——可选分布式单飞锁（复用 `pkg/lock`）、任务 panic 兜底（后台旁路任务不该带崩服务）、任务注册与执行的统一日志。已落地三项：**①首页热度榜预热**（feed，每 1min，单飞）把未登录首页的实时回源挪到缓存过期之前，读请求恒命中缓存、顺带刷新降级快照；**②播放量批量落库**（video，每 10s）把增量先累加进 Redis Hash，再由任务用 Lua `HGETALL + DEL` 原子取走并拼一条 `CASE WHEN` 批量写回——详情读路径不再同步写库（消除行热点），落库失败把增量还回缓冲不丢计数；**③过期上传清理**（video，每小时 + 启动即跑一轮）调度从 `time.Ticker` 迁到 cron，保留原有 `pkg/lock` 单飞口径。开关与周期见配置 `jobs`（**缺省关闭**，需显式 `enabled: true`，避免后台写意外发生）。实机验证：连打 3 次视频详情后 Redis 缓冲为 `{视频: 3}` 而库中计数**不变**，下一轮 `*/10` 落库后 `+3`、缓冲清空；feed 每分钟刷新热度榜（3 条）并同步刷新 24h 降级快照；`go test -tags integration ./internal/video/handler/...` 覆盖缓冲→落库全链路、批量 SQL（两行各带不同增量）、清理入口回收过期会话
> - ✅ 压测基线（k6）：压的是**网关读入口**——首页信息流 `GET /api/v1/feed` + 视频详情 `GET /api/v1/videos/:id`，70% / 30% 混合、固定到达率打流。脚本 `test/loadtest/read-path.js`，数据与结论见 [`test/loadtest/results/README.md`](test/loadtest/results/README.md)。额度内（10 rps）**0 失败**，feed P95 2.0ms / 详情 P95 16.2ms；超额度（200 rps / 60s）**89.7% 被限流拒绝（429）**，放行的 1239 次 = 桶容量 40 + 速率 20×60s，与配置分毫不差。结论：当前配置下的瓶颈是限流本身而非服务——网关 CPU 0.066 core / RSS 48MB，各服务 RSS 34–48MB、CPU≈0。**口径说明**：这是「生产配置下的过载基线」，压测流量来自单一源 IP、被按 IP 限流挡住，**服务的裸容量（限流之外）未测**——这一项**已决定不再补测**，当作**已知边界**记录下来（演示环境的价值在于「验证保护措施生效 + 读路径延迟基线」，追后端容量上限对非商用演示无收益），详见 results 文档「未覆盖 / 已知边界」
> - ✅ 邮件验证码真实投递（`pkg/mailer` 的 smtp 驱动，**阶段四清单外追加**——补上此前唯一「发不出去」的缺口）：在此之前只有 `log` 驱动，验证码只打进日志，**公网部署下普通用户根本收不到注册验证码**。现补齐 smtp 驱动：三种 TLS 模式（`ssl` 465 连上即握手 / `starttls` 587 / `none` 仅限本机调试，指向外网直接拒绝），端口留空时按模式推断；报文按 RFC 5322 / MIME 组装——中文主题与展示名走 RFC 2047 编码字、正文 base64（避开 998 字节行长限制与中文被中继当控制字符处理的坑）、CRLF 换行；口令只走环境变量 `CHUANGYUE_MAIL_PASSWORD`，并给单次投递设总超时（默认 10s，取 ctx deadline 与它更早者），避免 SMTP 无响应把发码请求挂死。配置错误一律在**启动时** fail-fast。**顺带修掉一个真实故障**：`deploy/k8s/overlays/prod/config.yaml` 原先写 `driver: log`，与 `env: prod` 冲突会让 **user 服务启动即 Fatal（crash-loop）**——已改为 smtp。验证两层：`go test ./pkg/mailer/` 用本机假 SMTP 服务器**真实走完** EHLO → STARTTLS → AUTH → MAIL / RCPT / DATA（含自签证书、错误口令、服务器不支持 STARTTLS 时**拒绝回退明文**、ctx 取消、静默服务器超时，共 11 个用例）；再以 `go run ./cmd/user -c deploy/k8s/overlays/prod/config.yaml` 做 A/B——改前停在「初始化邮件发送器失败」，改后已越过该步（随后因本地无 `etcd` DNS 按预期失败）。**实测发信已闭环**：真实服务商最终选用 **Resend**（放弃阿里云邮件推送——域名备案门槛高、免费域名又被拒），域名 `chuangyue.me` 通过验证后向**真实 QQ 邮箱投递成功**——`POST /api/v1/auth/email/code` 返 HTTP 200，user 服务日志出现「邮件已投递 `{"driver":"smtp","to":"1***5@qq.com"}`」（日志**刻意只记脱敏收件人与主题、绝不记正文**，验证码只在真实邮箱里），收件邮箱实际收到验证码。这条链路的边界**已关闭**，详见《邮箱注册实现逻辑》11.4「已完成的一轮」（本地 `docs/`，不入库）。**仍未改**：异步投递（投递失败需释放验证码与冷却，故保持同步）
> - ✅ 忘记密码 / 重置密码（**阶段四清单外追加**——此前「忘记密码」只有设计没有实现，用户一旦忘密码账号就废了）：发码接口泛化为 `SendCode(purpose)`，`reset` 用途与 `register` **语义刻意相反**——register 在邮箱已注册时报错，reset 无论邮箱是否存在都返回**完全同形**的成功响应，且未注册的邮箱不发信（否则这个接口就成了「批量探测哪些邮箱注册过」的工具）；未注册分支仍照常写码并占用冷却与小时配额，让限流行为一致，不留「打得久了会限流」这种侧信道。重置走匿名接口 `POST /auth/password/reset`（用户正是登不进来才走这条路），**不返回令牌**——它刚吊销完旧会话，再发一枚等于把刚吊销的还回去。**安全上的实质改动**：用户表加 `token_version`，签发令牌时写进 claims（`tv`）、**刷新时比对**，重置密码把它 +1，于是**重置前签发的刷新令牌立即失效**（这补上了《登录状态保持文档》里挂了很久的「改密即全端失效」的一半）。刻意只做刷新侧：刷新本来就要读用户行，比对零额外查询；而 access 校验在网关侧、手上没有用户表，要查版本就得给每个请求加一次远程调用——**旧 access 仍能活到自然过期（≤15 分钟），这是如实记录的折中，不是完整吊销**。同理改密（`PUT /users/me/password`）刻意**不**推进版本，免得把本人也踢下线。存量令牌没有 `tv` 声明、反序列化为 0，与用户表默认值一致，**升级不会把在线用户踢下线**。前端在登录弹框加「忘记密码？」入口与重置表单。验证：集成测试 `TestGRPCAuthAndProfileViaEtcd` 新增第 ⑩ 段（防枚举同形、验证码一次性、新密码可用、旧密码失效、旧刷新令牌被拒）；再走一遍真实 HTTP 全链路——`POST /auth/refresh` 带上重置前的 Cookie 返回 **401 / 20004**，新密码登录返回的令牌里 `"tv":1`
> - ✅ 用户管理（**阶段四清单外追加**——此前 `user.status` 字段与登录校验都在，却没有任何接口能改它，遇到恶意账号只能进库改 SQL）：新增 `GET /admin/users`（关键词匹配用户名 / 邮箱 / 昵称，role / status 可选过滤）与 `PATCH /admin/users/{id}`（改 status / role，两个字段都可选）。三处刻意的设计：**role / status 用指针而不是 0 兜底**——0 既是「普通用户」也是「已禁用」，拿它当「不过滤」会让写错参数的人以为自己看到了全部用户；**不允许改自己**（否则管理员可以把自己降级或封禁，把自己锁在门外），前端那一行直接显示「不能修改自己」而不是给个必然报错的按钮；**生效时机如实标注**——访问令牌是无状态的，封禁与改角色都要等下一次登录 / 刷新才生效，已签发的那枚最长还能用 15 分钟（刷新会读库并按新状态签发或拒绝）。前端新增 `/admin/users` 页（搜索、角色 / 状态双筛选、分页、封禁 / 解封 / 升权 / 降权，破坏性操作走确认弹框；改完若该行不再符合当前筛选就就地移除并修正总数）。验证：集成测试 `TestGRPCAdminUserManagement`（过滤 / 分页 / 封禁后登录被拒 / 解封恢复 / 升权 / 越界取值 / 空请求 / 不存在的用户 / 自我保护）；实机 HTTP 八步全通，浏览器里点完搜索、两个确认弹框与「当前账号」行也符合预期——**这一步还抓到一个真 bug**：网关的 DTO 映射漏了 `status`，列表里所有人都显示「已禁用」，已修
> - ✅ 删视频连带清理互动数据（**阶段四清单外追加**——此前删视频只删自己的行和磁盘文件，评论 / 弹幕 / 点赞 / 收藏 / 稍后再看 / 历史 / 计数 / 通知全留在互动库成孤儿行）：视频域在「记录已删、文件已清」后投递 `chuangyue.video.deleted`（只带 `video_id`），互动域消费后在一个事务里按 `video_id` 删干净（评论点赞没有 video_id，靠子查询按评论 id 定位）。**为什么不直接 RPC**：互动域本来就依赖视频域，再加一条视频 → 互动的同步调用就形成了环；事件是单向的，且消费侧天然可幂等重放（`DELETE … WHERE video_id = ?`，至少一次投递不会重复删错）。**投递失败只记 ERROR 日志、不回滚删除**——视频已经没了，让接口报错只会让管理员对着「视频不存在」反复重试；消费侧对「反序列化失败」返回 nil 跳过（毒消息不能让分区永久卡住），对「清库失败」才交给消费者退避重投。验证：`TestPurgeVideoData`（九张表清零、评论点赞按子查询清掉、别的视频一行不动、重复清理幂等、毒消息跳过、非法 id 拒绝）；实机跑通完整 Kafka 链路——`DELETE /admin/videos/{id}` → 视频记录 0 行、九张互动表全部归零、interaction 日志出现「视频删除事件已处理」
> - ✅ 镜像发布后的滚动更新（`deploy/scripts/rollout.sh`，**上线硬缺口补齐**——此前 CI 只构建推送 8 个镜像到 GHCR，把集群切到新版本只能照 `deploy/k8s/README.md` 的命令逐条手工敲）：给一个 tag，脚本用 `kubectl set image` 逐个改 Deployment 的 Pod 模板，再 `kubectl rollout status` 等各服务滚动完成；**顺序把 web 放在最后**，缩短「新前端打旧后端」的错配窗口；任一服务超时以非 0 退出。取舍：命令式 set image **不改动仓库里被跟踪的 kustomization.yaml**，代价是与 overlay 的 `newTag` 声明状态会分叉——要固化的版本请改 overlay `newTag` 并提交，脚本更适合「发布刚推上去的镜像」或「用 `sha-` 标签回滚」。`DRY_RUN=1` 只打印不执行。校验：`bash -n` 通过
> - ✅ 集群内定时备份（`deploy/k8s/backup`，**上线硬缺口补齐**——此前只有宿主侧的 `deploy/scripts/backup.sh`，纯集群部署没有可 apply 的调度清单）：一个 CronJob 每天 03:30 在集群内把两个业务库与上传卷打成归档、写进独立 PVC `chuangyue-backups` 并按保留期（默认 7 天）清理旧归档。备份 Pod 用公共镜像 `mysql:8.0`（自带 `mysqldump` / `gzip` / `tar` 且脚本启动自检，无需 imagePullSecret），root 口令取自 Secret `chuangyue-secrets`；因要挂上传卷（RWO）而带 `nodeSelector: chuangyue.io/storage=true`。刻意三处：与宿主版是**两条通道、不混用**（半截结果最难排查）；**不负责异地化**（归档与数据同盘，仍需另配 rsync / ossutil）；保留期清理用纯 shell + `date -r` 判龄而非 `find`（少一个「精简镜像里没有」的依赖）。校验：`kubectl kustomize deploy/k8s/backup` 渲染通过（ConfigMap 内联脚本、PVC / CronJob 名字对齐）、`bash -n` 通过
> - ✅ 备份恢复演练（**上线前必做项补齐**——此前 `deploy/README.md` 的「诚实边界」只验证到「归档形状支持恢复命令」，没做过完整灌库）：用 `deploy/scripts/backup.sh`（docker 模式）产出两库 + 上传目录归档，`gzip -t` / `tar -tzf` 校验通过；再把归档灌进一个**全新的 `mysql:8.0` 干净实例**，逐库核对「表数 + 各表 `COUNT(*)` 合计」——`chuangyue` 14 表 / 63 行、`chuangyue_interaction` 9 表 / 48 行，与源库**完全一致**。如实说明：演练只在**本机容器**做过，上线前仍建议在**目标环境的独立实例**上再核一次（不同机器的权限 / 版本差异可能暴露本机没出现的问题）

---

## 文档

- 设计文档与运维文档（《流媒体平台设计文档》——架构 / 服务设计 / 数据模型 / 接口清单 / 实施路线；《云服务器集群方案》——选购 / k3s 设计 / 成本优化 / 「从购买到部署」分阶段清单；各模块实现逻辑）保留在**本地 `docs/`**，按约定**不随仓库公开**。
- 服务器真实 IP / 密码记录在本地 `docs/服务器信息记录.local.md`（`.gitignore` 已忽略，不会提交）。

---

## 文档说明

- 本文档为窗月项目的设计说明，用于指导开发实施。
- 架构决策如与实施过程中的实际情况冲突，以代码实现为准并回写设计文档。
