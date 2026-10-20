# 窗月 · 可观测栈（Prometheus + Alertmanager + Grafana + Jaeger）

`pkg/metrics` 负责**产出**指标、`pkg/tracing` 负责**上报**链路，本目录负责**收集、查看与报警**：
Prometheus 抓取 `/metrics` 并按规则判定告警，Alertmanager 负责分组 / 抑制 / 通知，
Grafana 展示看板，Jaeger 接收并展示 OTLP 链路 span。
四者都挂在 compose 的 `monitoring` profile 下，是观察面、不参与业务，
因此和 `app` / `full` 分开启停。Jaeger 用官方 all-in-one 镜像（内存存储，重启即丢），
无需额外配置文件，故本目录下只有 Prometheus / Alertmanager / Grafana 的落盘配置。

> 一句话区分「看板」与「告警」：看板回答**现在怎么样**（要有人主动去看），
> 告警回答**什么时候该有人来看**（指标自己会喊人）。两者不是替代关系——
> 没有看板就只能看到「某个阈值被撞破了」，没有告警就只能守在屏幕前。

```
deploy/monitoring/
├── prometheus/
│   ├── prometheus.yml               # 抓取配置（8 个目标）+ 规则文件与 Alertmanager 地址
│   └── alerts.yml                   # 告警规则（5 组 11 条）
├── alertmanager/
│   └── alertmanager.yml             # 分组 / 抑制 / 路由（本地不外发通知）
└── grafana/
    ├── provisioning/
    │   ├── datasources/prometheus.yml   # 数据源（uid 固定为 prometheus）
    │   └── dashboards/dashboards.yml    # 看板装载（读下面 dashboards/ 目录）
    └── dashboards/
        ├── gateway-overview.json        # 窗月 · 网关总览
        └── protection.json              # 窗月 · 保护措施与下游健康
```

## 启动

```bash
# 监控栈依赖业务进程暴露的 /metrics，因此与 app profile 一起起
docker compose -f deploy/compose/docker-compose.yml --profile app --profile monitoring up -d

# 只想先看监控栈（目标会是 DOWN，但能验证装载是否正常）
docker compose -f deploy/compose/docker-compose.yml --profile monitoring up -d
```

- Prometheus：<http://127.0.0.1:9090>（Status → Targets 可看 8 个目标的抓取状态，Alerts 页可看规则与告警状态）
- Alertmanager：<http://127.0.0.1:9093>（告警的分组结果、抑制与静默都在这里操作）
- Grafana：<http://127.0.0.1:3000>，两个看板位于 Dashboards → 浏览（**无需手工导入**，由 provisioning 装载）
- Jaeger：<http://127.0.0.1:16686>（Search → Service 选 `gateway` → Find Traces，即可看跨服务调用树）

### 改完配置先校验再重启

两份配置都有官方校验工具，改完先跑一遍，避免靠重启容器试错：

```bash
# 规则文件（改 alerts.yml 后）
docker run --rm --entrypoint promtool \
  -v "$PWD/deploy/monitoring/prometheus:/cfg:ro" prom/prometheus:v3.1.0 check rules /cfg/alerts.yml

# Alertmanager 配置（改 alertmanager.yml 后）
docker run --rm --entrypoint amtool \
  -v "$PWD/deploy/monitoring/alertmanager:/cfg:ro" prom/alertmanager:v0.28.1 check-config /cfg/alertmanager.yml
```

> 镜像的 entrypoint 是 prometheus / alertmanager 本体，直接 `docker run <image> promtool …`
> 会被当成主程序的参数而报 `unexpected promtool`——必须用 `--entrypoint` 换掉。

改完重启生效（Prometheus 未开 `--web.enable-lifecycle`，改规则要重建容器）：

```bash
docker compose -f deploy/compose/docker-compose.yml --profile monitoring up -d --no-deps --force-recreate prometheus
```


## 链路追踪（Jaeger）

| 端口 | 用途 |
|---|---|
| `4317` | OTLP gRPC：各服务经它上报 span（config `tracing.endpoint` 指向它） |
| `4318` | OTLP HTTP：备用入口 |
| `16686` | 查询 UI |

- 是否上报由各服务的 `tracing.endpoint` 决定：**留空即禁用**（不产生 span，日志 trace_id 照旧）。
- 采样是 `ParentBased(TraceIDRatioBased)`：根 span 按 `sample_ratio` 抽，子 span 一律沿用父决策，
  因此不会出现「一半有、一半断」的链路。
- 网关的 HTTP 请求是根 span，随后每次出站 gRPC 调用（`otelgrpc` 客户端钩子）都是它的子 span，
  下游服务的入站 RPC（服务端钩子）再接一层——于是首页一次请求就能看到「gateway → feed → video」整棵树。
- 日志与链路的对齐：网关把本次请求 span 的 TraceID 作为日志 `trace_id`，**两者是同一个值**，可互相跳转。

## 抓取目标（`prometheus/prometheus.yml`）

| job | 目标 | 内容 |
|---|---|---|
| `prometheus` | `localhost:9090` | 自身运行指标（排查「为什么没数据」先看它） |
| `gateway` | `gateway:8080` | HTTP 请求数 / 耗时 / 在途；**限流拒绝数、熔断器状态只在这里** |
| `user` | `user:9203` | gRPC 服务端 + Go 运行时 |
| `video` | `video:9202` | 同上 |
| `interaction` | `interaction:9201` | 同上 |
| `feed` | `feed:9204` | 同上 |
| `search` | `search:9205` | 同上 |
| `transcode-worker` | `transcode-worker:9206` | 只有指标端口，无 gRPC 端口 |

目标名是 **compose 服务名**（容器内互访靠 compose 网络）。若服务改为宿主机 `go run`，
把 targets 换成 `host.docker.internal:92xx`；端口必须与各服务启动参数 `-metrics-addr` 一致。

> 一个容易踩的坑：`chuangyue_ratelimit_rejected_total` **只有网关暴露**（限流中间件只在网关挂载），
> 而 `chuangyue_breaker_state` 是**凡持有 gRPC 客户端的进程都会暴露**（网关 5 条、feed / interaction /
> search / transcode-worker 各若干条）——同一个下游会出现多条序列，看板查询必须按 `service` 取
> `max` 聚合，否则泳道会重复。`chuangyue_grpc_client_*`（出站指标）同理只存在于调用方进程。

## 两块看板

| 看板 | 回答什么问题 |
|---|---|
| **网关总览** | 现在多少 QPS、P99 多慢、错误率多少、哪个路由在放大、目标是否全在线 |
| **保护措施与下游健康** | 限流挡了多少（按 IP / 用户维度）、熔断器有没有打开、gRPC 出站失败落在哪个下游、出站与服务端延迟差在哪 |

看板以文件形式提供（`provisioning` + JSON），随仓库走版本管理，重建容器即恢复。
`allowUiUpdates: true` 只是为了本地调看板方便——**改完要回写到仓库里的 JSON**，否则重启就丢。

## 告警规则（`prometheus/alerts.yml`）

Prometheus 按 `evaluation_interval`（15s）执行规则里的表达式，条件成立即产生一条告警，
推给 Alertmanager 处置。规则共 5 组 11 条，按「出问题时先看哪一类」排序：

| 分组 | 告警 | 判据（`for` 表示需持续多久才算数） | 级别 |
|---|---|---|---|
| 可用性 | `TargetDown` | `up == 0` 持续 1m | critical |
| 入口 | `GatewayHighErrorRate` | 按路由 5xx 占比 > 5%（且该路由 > 0.1 rps）持续 5m | critical |
| 入口 | `GatewayHighLatency` | 按路由 P99 > 1s 持续 5m | warning |
| 入口 | `RateLimitRejectionHigh` | 限流拒绝占比 > 20% 持续 5m（按 ip / user 分维度） | warning |
| 依赖 | `CircuitBreakerNotClosed` | 任一下游的熔断器未闭合持续 2m（按 service 取 max） | warning |
| 依赖 | `DownstreamCallFailing` | 出站失败率 > 10% 持续 5m（按 target） | warning |
| 依赖 | `DownstreamCallSlow` | 出站 P99 > 3s 持续 5m（按 target） | warning |
| 运行时 | `GoroutineTooMany` | `go_goroutines > 1000` 持续 10m | warning |
| 运行时 | `ProcessMemoryHigh` | 进程 RSS > 512MiB 持续 10m | warning |
| 后台任务 | `TranscodeFailed` | 15m 内出现转码最终失败（`increase(…{result="failed"}[15m]) > 0`）持续 1m | warning |
| 后台任务 | `CronJobStalled` | 最近成功距今 > 2×调度间隔 + 60s 持续 3m | warning |

几条一以贯之的写法，都是踩过的坑换来的：

- **比例类告警都带最小流量护栏**（`and … > 0.1`）。低流量路由上「1 个请求里有 1 个 5xx」就是 100%，
  不加护栏会被小样本放大成假警报；护栏要求分母约达 6 次/分钟才参与判断。
- **5xx 与 429 分开统计**。429 是保护措施在正常工作（系统主动拒绝），不算「错误」；
  混进错误率里会让这条告警失去意义，所以前者进 `GatewayHighErrorRate`，后者单独用拒绝占比盯。
- **熔断告警判的是「未闭合」而不是「已打开」**。打开态只维持 `open_timeout`（10s）就转半开，
  写成「等 `== 2` 持续 2 分钟」这条规则永远不会触发——窗口撑不到。判据取 `> 0`（半开或打开），
  语义是「这个下游持续不健康」，这才是能等满 2 分钟的现象。
- **按维度下钻，而不是只看全局**。全局错误率会被健康接口稀释，而「哪个路由 / 哪个下游在坏」
  才是能直接动手的信息；同时 `service` 这种会在多处出现的指标必须 `max by` 聚合，
  否则同一下游会出现多条重复序列（凡持有该下游客户端的进程各暴露一份）。
- **后台任务看「没有发生」而不是「报错了」**。转码 Worker 与周期任务都不经过 HTTP / gRPC，
  它们的失败与停摆不在入口错误率或依赖失败率里，必须由任务自身上报。停摆判据取
  「最近成功时间戳 + 调度间隔」（`time() - max by (job)(last_success) > 2*interval + 60`），
  而不是「有没有报错」——任务可能只是不再被调度、卡死或进程被杀，这几种都不产生错误计数；
  按 `job` 取 `max` 是因为多副本下单飞锁会让一个实例长期只记 `skipped`，另一实例正常 `success`。
- **计数要在结果落库成功之后再 `inc`**。转码回写失败的任务会重投，若在转码结束时就计数，
  同一次转码会被算成多次，`TranscodeFailed` 随之误报。

### 分组与抑制（`alertmanager/alertmanager.yml`）

判定归 Prometheus，处置归 Alertmanager，两者是两件事：

- **分组** `group_by: [alertname, service, target, path]`——同一问题合并成一条通知，
  不同来源分开。刻意**不用 instance**：滚动更新会让 instance 变化，同一故障会被拆成多条。
- **通知节奏**：`group_wait: 10s`（给同一批告警一个凑齐的时间窗）、
  `group_interval: 5m`、`repeat_interval: 4h`（critical 收紧到 1h）。重复间隔设成小时级是刻意的：
  故障没修好时反复提醒只会让人麻木，宁可靠 UI 里的「未恢复」状态持续可见。
- **抑制**：`TargetDown` 存在时，压掉同一 `service` 的依赖类告警。
  因为「某服务没了」必然连带出一串「调用它的失败率升高 / 熔断器打开」——那些是同一件事的不同侧面，
  不抑制的话根因反而被噪音淹掉。抑制靠 `equal: ["service"]` 对齐标签，
  为此 `TargetDown` 把 `job` 映射成了 `service`，`DownstreamCallFailing / Slow` 也补了等值的 `service`
  （它们的指标标签本叫 `target`）——**抑制的前提是两侧有同名标签，标签对不上就永远抑制不了**。
  边界：入口类告警按 `path` 维度，与具体服务无同名标签可对齐，因此不做跨维度抑制——
  宁可在界面上多看一条，也不错杀。

### 本地验证告警

告警链路最容易「配了但从没响过」，所以下面这套动作值得手动走一遍：

```bash
# 1. 规则与配置是否装载
curl -s localhost:9090/api/v1/rules | jq '.data.groups[].name'      # 5 个组
curl -s localhost:9090/api/v1/alertmanagers | jq '.data.activeAlertmanagers'

# 2. 制造一个警报：停掉某个服务，TargetDown 在 for(1m) 后进入 firing
docker stop chuangyue-video

# 3. 同时打流量，逼出派生告警（出站失败率 / 熔断器）——用于验证抑制
#    注意 for 最长为 5m，所以流量要持续打 5 分钟以上
while true; do curl -s -o /dev/null localhost:8080/api/v1/videos/2109501405638168578; sleep 0.3; done

# 4. 看结果：Prometheus 里 4 条都在 firing；Alertmanager 里只剩 TargetDown（其余被抑制）
curl -s localhost:9090/api/v1/alerts | jq '.data.alerts[] | {name: .labels.alertname, state}'
curl -s 'localhost:9093/api/v2/alerts' | jq '.[].labels.alertname'

# 5. 恢复
docker start chuangyue-video
```

第 4 步是这套配置最值得看的一处：Prometheus 侧 4 条告警同时 firing，
Alertmanager 侧只剩 1 条（`TargetDown`）——**根因可见、噪音被压掉**，这就是抑制规则在做的事。

**一次真实演练的记录**（2026-10-15，本地 compose 全栈）：

| 时刻 | 事件 |
|---|---|
| T+0 | `docker stop chuangyue-video`，同时以约 3 req/s 持续请求视频详情 |
| T+1m | `TargetDown{service="video"}` 进入 firing（`for: 1m`） |
| T+2m | `CircuitBreakerNotClosed{service="video"}` 进入 firing（`for: 2m`） |
| T+5m | `DownstreamCallFailing{target="video"}`、`GatewayHighErrorRate{path="/api/v1/videos/:id"}` 进入 firing（`for: 5m`） |

此时的对照（`duration` 与 `health` 均为 `ok`，4 条规则全部按预期触发）：

| Prometheus 侧（4 条 firing） | Alertmanager 侧 | 说明 |
|---|---|---|
| `TargetDown`（critical，service=video） | **active** | 根因，唯一该被通知的一条 |
| `CircuitBreakerNotClosed`（warning，service=video） | **suppressed** | `inhibitedBy` = `TargetDown` 的 fingerprint |
| `DownstreamCallFailing`（warning，service=video，target=video） | **suppressed** | 同上 |
| `GatewayHighErrorRate`（critical，path=/api/v1/videos/:id） | **active** | 入口维度无同名标签可对齐，按设计不抑制 |

`docker start chuangyue-video` 之后：`TargetDown` 立即恢复（`up` 变回 1），
抑制随之解除——另两条从 suppressed 变回 active，并在 5 分钟窗口滑出后自行恢复。
这一串「先升后降」说明的不只是「能响」，还有**能自己停**：漏掉恢复这一步的告警系统，
最后一定会被人关掉。

### 后台任务告警（原先的缺口已补齐）

转码 Worker 与周期任务都不经过 HTTP / gRPC，它们的失败与停摆不会出现在入口错误率或依赖失败率里，
必须由任务自身上报指标。原先设计上声明过、却**没有指标可依**的两处现已落地：

- **转码失败 `TranscodeFailed`**：Worker 新增 `chuangyue_transcode_jobs_total{result}`，
  在结果**落库成功之后**才计数（否则回写失败的任务会重投，同一次转码被算成多次）。
  判据 `increase(chuangyue_transcode_jobs_total{result="failed"}[15m]) > 0`——单次失败就会让一个视频
  停在「转码失败」等待管理员处理，故不做比例判断，15 分钟内出现即告警，窗口滑出后自动恢复。
- **周期任务停摆 `CronJobStalled`**：`pkg/cron` 上报 `chuangyue_cron_job_last_success_timestamp_seconds`、
  `chuangyue_cron_job_interval_seconds`、`chuangyue_cron_job_runs_total{job,result}` 三组指标。
  判据 `time() - max by (job)(last_success) > 2 * max by (job)(interval) + 60`——用**时间戳而非错误计数**，
  因为任务可能只是不再被调度、卡死或进程被杀，这几种都不产生错误；按 `job` 取 `max` 是因为单飞锁
  会让一个实例长期只记 `skipped`，另一个正常 `success`。视频 / feed 暴露的 `:92xx/metrics`、
  Worker 暴露的 `:9206/metrics` 均可直接抓到这三组指标，看板「保护措施与下游健康」另有两块面板
  （定时任务距上次成功、转码结果速率）。


## 阈值校准（本目录的主要用途）

配置里的限流（IP 20/s 突发 40、用户 10/s 突发 20）与熔断（连续 5 次失败 / 冷却 10s）是按经验拍的。
有了真实曲线才能定得准，做法：

1. 压一波正常流量，读「网关总览」的 P99 与「保护措施」的出站 P99，确认无请求接近 5s 超时上限；
2. 用脚本打超过阈值的请求，读限流拒绝速率与 `status=429` 落在哪个路由 —— 若是登录 / 刷新令牌这类
   必要接口被挡，说明阈值过紧或该接口应免限流；
3. 人为让某个下游不可用（如停 `feed` 容器），读熔断器状态泳道：红色持续多久、半开多久恢复 ——
   据此调「连续失败次数」与「冷却时间」。

告警阈值（5% / 1s / 20% / 10% / 3s 等）同样是**起点**，不是结论，校准路径与上面一致——
第 3 步正好可以复用来做告警演练（见上一节的记录）。判读时先分清两种情形再动手：
「系统真出问题」改代码，「阈值过紧」改 `alerts.yml`。留一个反例当警示：
若 5xx 阈值定成 0.1%，任何一次偶发的超时都会把人叫起来，几天之内这套告警就会被静音——
**误报的代价不是多看一眼，而是告警从此不再被信任**。

## 凭据

Grafana 首次登录账号 `admin`，口令取环境变量 `GRAFANA_ADMIN_PASSWORD`（未设置时回落到默认 `admin`，
Grafana 会要求首次登录后修改）。**生产必须显式设置**，例如：

```bash
export GRAFANA_ADMIN_PASSWORD='<自行设置>'
```

仓库里不落任何口令；`GF_USERS_ALLOW_SIGN_UP=false` 关闭自助注册。

## 生产注意

- **别把 Prometheus / Alertmanager / Grafana / Jaeger 暴露到公网**：集群里应只在监控网段可达（本目录的端口映射仅供本地）。
  Alertmanager 尤其要注意——它的 UI 允许**静默（silence）**告警，暴露出去等于把「关掉报警」的能力交出去。
- **告警渠道与凭据**：本文件的 `alertmanager.yml` 只有一个不外发的 `local` receiver，接入真实渠道的写法见其文末注释。
  收件凭证（SMTP 口令、机器人 Webhook token）走环境变量或集群 Secret，**不要落进仓库**；
  通知里的跳转链接由 `--web.external-url` 决定，换环境要一起改，否则点开的链接指向本机。
- `/metrics` 与 `/debug/pprof` 同样不该对公网开放：前者含路由与调用量信息，后者的 profile / trace 会耗 CPU，
  生产应在接入层限制来源网段。
- Jaeger 的 all-in-one 是**内存存储**：重启即丢，且单实例不抗压——生产应换成带持久化后端的部署
  （ClickHouse / Elasticsearch）并按需扩采集器，而不是把这里的 compose 定义照搬进集群。
- 上 K8s 时监控栈属**集群级**能力：用 kube-prometheus-stack（含 Prometheus Operator 与 Alertmanager），
  按 Pod 注解 / ServiceMonitor 发现目标、用 `PrometheusRule` CRD 承载规则，
  而不是把这里的 compose 定义照搬进集群——但**规则本身可复用**：`alerts.yml` 里的表达式与目标环境无关，
  换过去只需把 `job` / `service` 标签的取值对齐集群里的命名。
