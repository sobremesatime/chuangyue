# test/loadtest · 读路径压测基线

对**网关读入口**（首页信息流 `GET /api/v1/feed` + 视频详情 `GET /api/v1/videos/:id`）的压测基线，
用于后续优化时对照「优化前是多少」。

## 结论

压测时间 2026-10-15，单机 Docker Desktop（Windows），栈为 compose `full + app + monitoring`，
样例数据为库中 3 条已上架视频，混合比 70% 信息流 / 30% 详情（`DETAIL_RATIO=0.3`）。

| 场景 | 目标到达率 | 实际放行 | 失败率(429) | feed p95 | feed p99 | 详情 p95 | 详情 p99 | 网关 CPU | 网关 RSS |
|---|---|---|---|---|---|---|---|---|---|
| 额度内 | 10 rps | 10.0 rps | 0% | 2.0 ms | 2.2 ms | 16.2 ms | 20.2 ms | — | — |
| 超额度 | 200 rps | 20.7 rps | 89.7% | 1.3 ms | 1.7 ms | 11.8 ms | 15.5 ms | 0.066 core | 48 MB |

> 超额度那行的「放行」口径：1239 次成功 ÷ 60s ≈ 20.7 rps。1239 恰好等于**桶容量 40 + 速率 20×60s**，
> 说明令牌桶行为与配置完全一致（见下方发现 1）。

### 关键发现

1. **当前配置下的瓶颈是限流本身，不是服务**。`ratelimit.ip_per_second=20 / ip_burst=40`，压测流量全部
   来自同一个源 IP（k6 容器），因此 200 rps 有 89.7% 被 429 打回，放行的请求数与令牌桶的理论值分毫不差。
   Prometheus 侧同口径可见：`chuangyue_http_requests_total{status="429"}` 远高于 `status="200"`，
   `chuangyue_ratelimit_rejected_total{dimension="ip"}` 与 429 速率一致。
2. **服务侧远未饱和**。即使在被压到 200 rps 的那一轮，被放行的请求延迟仍然很低，且各服务资源占用几乎为零
   （见下表）。也就是说：限流挡住的量，后端完全吃得下 —— 这个「余量」正是后续要量化的对象。
3. **详情接口比信息流贵约一个数量级**（额度内 p95 16.2 ms vs 2.0 ms）。原因是两条链路的成本结构不同：
   信息流走推荐服务的 Redis 缓存；详情是「读一次库 + **写一次库**」的同步链路 —— 返回前会同步执行
   `UPDATE video SET view_count = view_count + 1`（[video.go](file:///d:/trae%20projects/chuangyue/internal/video/repository/video.go#L213-L219)）。
   库中只有 3 条样例视频，意味着所有详情请求的写全部竞争这 3 行。本基线未单独量化行锁等待，
   但这是详情延迟的结构性来源，也是后续最值得优化的一处。
4. **资源占用（近 3 分钟均值，覆盖两轮压测）** —— 后端离瓶颈很远，优化空间在链路设计而非机器配置：

   | 服务 | CPU (core) | RSS |
   |---|---|---|
   | gateway | 0.066 | 48.8 MB |
   | video | 0.010 | 43.6 MB |
   | feed | 0.007 | 38.1 MB |
   | interaction | 0.001 | 38.7 MB |
   | user | 0.001 | 40.7 MB |
   | search | 0.001 | 34.9 MB |

## 复现方式

`grafana/k6` 跑在容器里、接入 compose 网络，直接用服务名访问网关（省掉 `host.docker.internal` 在
Windows 上的差异）：

```bash
docker run --rm --network chuangyue_default \
  -v "<repo>/test/loadtest:/scripts" \
  -e RATE=10 -e DURATION=60s -e BASE_URL=http://gateway:8080 \
  grafana/k6 run /scripts/read-path.js
```

脚本参数见 [read-path.js](file:///d:/trae%20projects/chuangyue/test/loadtest/read-path.js)。
每轮结束会把精简后的 summary 写到 `results/<RATE>rps.json`；`RATE` 与 `DURATION` 的组合即一份可复现的基线记录。

> **复现时的预期行为**：脚本的阈值把 429 计入 `http_req_failed`，因此在网关限流开启、且 `RATE` 超过
> 按 IP 额度（默认 20 rps）时，k6 会以**退出码 99** 结束并打印 `thresholds ... have been crossed`。
> 这是「保护确实生效」的正常信号，不是脚本故障；把它当成功复现即可（例如 `200rps.json` 那一轮）。

## 未覆盖 / 已知边界

- **服务裸容量（限流之外）**：单机压测只有一个源 IP，压力被限流阈值挡在 20 rps，量不到后端真实上限；
  要补上需要**临时抬高限流阈值**（测完须还原并重建网关）或改用多源 IP 打流。**本项已决定不补测**——
  上表因此只反映「生产配置下的过载表现」，不代表容量上限，按**已知边界**记录而非待办
  （演示环境的价值在于验证保护措施生效与读路径延迟基线，追容量上限对非商用演示无收益）。
- 登录后的写入链路（点赞 / 收藏 / 评论 / 上报观看历史）与管理员上传：写路径变量多（身份、文件），
  不适合做基线，未纳入。
- 缓存击穿 / 跨实例惊群、Kafka 回灌（播放量批量落库）等场景：后续按需补。

## 数据文件

| 文件 | 说明 |
|---|---|
| `10rps.json` | 额度内一轮（10 rps / 60s）：0 失败，读路径延迟基线 |
| `200rps.json` | 超额度一轮（200 rps / 60s）：89.7% 被限流，验证保护生效 |
