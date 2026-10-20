# cmd · 服务入口

每个子目录对应一个可执行程序，**只放 `main.go`**。职责是装配依赖并启动服务，不写业务逻辑。

| 目录 | 程序 | 监听 | 说明 |
| --- | --- | --- | --- |
| `gateway/` | API 网关 | HTTP `:8080` | 唯一对外入口，把 HTTP 翻译成对内部服务的 gRPC 调用，并按原口径映射回 JSON |
| `user/` | 用户服务 | gRPC `:9103` | 注册、登录、Token 刷新、用户资料、跨服务用户展示信息 |
| `video/` | 视频服务 | gRPC `:9102` | 视频元数据、分片上传、播放地址、举报处理 |
| `interaction/` | 互动服务 | gRPC `:9101` | 点赞、收藏、稍后再看、观看历史、评论、弹幕、站内通知 |
| `feed/` | 推荐服务 | gRPC `:9104` | 首页信息流（经网关 `GET /api/v1/feed` 对外） |
| `search/` | 搜索服务 | gRPC `:9105` | 视频检索（经网关 `GET /api/v1/search/videos` 对外） |
| `transcode-worker/` | 转码 Worker | — | 消费 Kafka，调用 FFmpeg 转码，状态经视频服务 gRPC 回写 |

服务之间**只认服务名、不写死 IP**：各进程启动时把自身地址注册到 Etcd，调用方经 `pkg/grpcclient` 拨号、由 `pkg/registry` 完成发现。注意 `gateway` 已是纯翻译层——它不持有任何业务库，全部接口都落到上述服务。

业务实现一律放在 `internal/` 下对应的业务域目录。
