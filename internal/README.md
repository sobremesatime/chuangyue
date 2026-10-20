# internal · 私有业务实现

Go 的 `internal` 目录对外部模块不可见，业务代码放在这里可避免被其他项目误引用。

## 组织方式

按**业务域**划分一级目录（而不是按技术层），每个业务域内部再分三层：

```
internal/<domain>/
├── handler/      # 接入层：参数校验、协议转换（HTTP / gRPC）
├── service/      # 业务逻辑层：编排、事务、领域规则
├── repository/   # 数据访问层：SQL / Redis / 外部调用
├── model/        # 领域模型
└── dto/          # 数据传输对象
```

| 业务域 | 对应服务 | 说明 |
| --- | --- | --- |
| `gateway/` | API 网关 | 只有 `handler/`（HTTP↔gRPC 翻译）与 `router/`（依赖注入）；**不含 service / repository / model**，不持有任何业务库 |
| `user/` | 用户服务 | 账号、资料、邮箱验证码 |
| `video/` | 视频服务 | 视频元数据、分片上传、举报、转码状态 |
| `interaction/` | 互动服务 | 评论、弹幕、点赞 / 收藏 / 稍后再看 / 观看历史、站内通知 |
| `feed/` | 推荐服务 | 首页推荐聚合（读视频域候选池 + 互动域用户信号重排） |
| `search/` | 搜索服务 | 视频检索（经视频服务检索，不持有业务表） |
| `transcode/` | 转码流水线 | FFmpeg 多码率 HLS 转码与任务消费 |

> 除网关外，各业务域都已独立成进程：其 `handler/` 里是 **gRPC 服务端实现**（`grpc_*.go`），对外 HTTP 只由网关承载。因此网关的 handler 不写业务逻辑——只做请求绑定、调用下游 gRPC、再把结果按原有 JSON 口径映射回来。

## 为什么按业务域而不是按层划分

将来拆分微服务时，整个业务域目录可以直接搬走，不用在 `handler/`、`service/` 之间挑文件。

## 依赖方向

`handler → service → repository`，只能单向依赖，禁止反向引用。
