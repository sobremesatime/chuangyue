# deploy · 部署

| 目录 | 用途 |
| --- | --- |
| `compose/` | 本地编排：基础设施（MySQL / Redis / Etcd / Kafka / MinIO）+ 应用服务（`app` profile） |
| `docker/` | 服务镜像：一个多阶段 `Dockerfile`，用 build arg `SERVICE` 选择要构建的 `cmd/` 子目录 |
| `nginx/` | 接入层：`nginx.conf`（前端静态 + `/api` 反代）与前端镜像 `Dockerfile`（构建出 `chuangyue/web`） |
| `k8s/base/` | K8s 基础清单（Kustomize）：接入层 + 应用 7 个进程的 Deployment / Service / ConfigMap / PVC；Secret 不入清单（只有 `secret.yaml.example` 模板） |
| `k8s/infra/` | 自建中间件：MySQL / Redis / Etcd / Kafka / MinIO 的单实例 StatefulSet（全部自建，Etcd 是服务发现中心、缺一不可） |
| `k8s/overlays/` | 按环境覆盖：`dev`（单副本、tag=dev）、`prod`（多副本、prod 配置、资源抬高、Ingress→web、k3s 本地卷适配） |
| `k8s/backup/` | 集群内定时备份：CronJob + 归档 PVC + 备份脚本（自建 MySQL 没有自动备份，靠它兜底） |
| `monitoring/` | 监控栈：Prometheus 抓取配置 + 告警规则（`alerts.yml`）+ Alertmanager 配置 + Grafana 数据源与两块看板（compose `monitoring` profile） |
| `scripts/` | 部署辅助脚本：`bootstrap-k8s.sh`（k8s 首次初始化：命名空间 → Secret → 中间件 → 灌库，带 `--dry-run` 与逐步确认）、`backup.sh`（宿主机侧备份：业务库 + 上传目录，含保留期清理）、`rollout.sh`（镜像推送后的滚动更新与回滚） |

## 集群：Kustomize 部署

应用清单只编排**接入层 + 应用进程**（`web` / `gateway` / 五个服务 / 转码 Worker）。
中间件（MySQL / Redis / Etcd / Kafka / MinIO）**全部自建**，统一用 `kubectl apply -k deploy/k8s/infra` 起一整套单实例 StatefulSet；
其中 **Etcd 是服务注册与发现的中心**，缺了它服务注册不上、网关发现不到后端，必须最先确保就绪。

首次部署的顺序是 **Secret → 中间件 → 建表 → 应用**，完整命令（含 Secret 怎么生成、
`migrations` 怎么灌库）见 [`k8s/README.md`](k8s/README.md#部署首次按顺序执行)——那几步漏一步都起不来。

```bash
# 只看渲染结果（不需要集群）
kubectl kustomize deploy/k8s/overlays/prod
```

> 首次部署之后的**滚动更新**（镜像已由 CI 推到 GHCR）：`./deploy/scripts/rollout.sh <tag>`，见 [`k8s/README.md`](k8s/README.md#发布与滚动更新)。

细节（前置依赖、镜像构建与推送、RWX 共享卷、ConfigMap/Secret、入口链路、与 Compose 的对照）见 [`k8s/README.md`](k8s/README.md)；
接入层的 `nginx.conf` 与前端镜像见 [`nginx/README.md`](nginx/README.md)。

## 本地：一键起全栈

```bash
# 基础设施 + 应用服务（网关 / 五个服务 / 转码 Worker）；首次需要 --build 构建镜像
docker compose -f deploy/compose/docker-compose.yml --profile app up -d --build

# 需要异步转码链路时，把 full（Kafka / MinIO）一起带上
docker compose -f deploy/compose/docker-compose.yml --profile full --profile app up -d --build
```

- 不带 `--profile app` 时行为不变：只起 MySQL / Redis / Etcd，服务照旧可以 `go run` 本地调试。
- 应用服务的配置在 [`compose/config.yaml`](compose/config.yaml)，运行时挂载到容器 `/app/config.yaml`，不打进镜像。
- 各服务的 `-advertise` 填的是 compose 服务名（如 `user:9103`）——容器间经 Etcd 按服务名互通，不能沿用默认的 `127.0.0.1`。
- 网关暴露在宿主 `8080`，与本地 `go run` 方式一致，前端无需改代理配置。

## 本地：监控栈

指标由 `pkg/metrics` 在网关 `/metrics`、各服务 `:92xx/metrics` 暴露；Prometheus 抓取并按规则判定告警、
Alertmanager 负责分组 / 抑制 / 通知、Grafana 展示，三者挂在 `monitoring` profile
（观察面，不参与业务，故与 `app` 分开启停）：

```bash
docker compose -f deploy/compose/docker-compose.yml --profile app --profile monitoring up -d
```

- Grafana <http://127.0.0.1:3000>（两块看板由 provisioning 自动装载，无需手工导入）
- Prometheus <http://127.0.0.1:9090>（Status → Targets 看抓取状态，Alerts 页看规则与告警）
- Alertmanager <http://127.0.0.1:9093>（告警分组结果；本地不外发通知，只在这里看）

细节（抓取目标、看板内容、告警规则、阈值校准方法、凭据与生产注意事项）见 [`monitoring/README.md`](monitoring/README.md)。

## 数据备份与恢复

自建 MySQL 没有云 RDS 的自动备份；一次误删表、一块盘挂掉，数据就没了。
[`scripts/backup.sh`](scripts/backup.sh) 把「业务库 + 上传目录」定期落成带时间戳的归档，并按保留期清理：

```bash
# docker 模式（默认；中间件跑在本机容器里——compose 与「自建中间件的单机部署」都适用）
MYSQL_PASSWORD='***' ./scripts/backup.sh

# k3s 部署：连库带卷一起从集群里取（需要你已有 kubectl 授权）
MYSQL_PASSWORD='***' BACKUP_MODE=k8s UPLOADS_POD_NAME=<挂着上传卷的 Pod> ./scripts/backup.sh

# 每天 03:30 备份
30 3 * * * MYSQL_PASSWORD='***' /opt/chuangyue/deploy/scripts/backup.sh >>/var/log/chuangyue-backup.log 2>&1
```

环境变量（`BACKUP_DIR` / `RETAIN_DAYS` / `DB_LIST` 以及各模式的容器名、Pod 名、直连地址）见脚本头部注释。

**纯集群、不进宿主**时改用集群内的 CronJob（备份 Pod 直接连 `mysql` Service 与上传卷，不需要 docker / kubectl）：

```bash
kubectl apply -k deploy/k8s/backup   # CronJob + 归档 PVC；每天 03:30 在集群内备份，保留期默认 7 天
```

两条通道**不混用**——「备份库成功、只备份文件失败」这种半截结果最难排查；取舍与说明见 [`k8s/README.md`](k8s/README.md#集群内定时备份可选)。

三个刻意的取舍：

- **两个库都备**：`chuangyue` 与拆库后的 `chuangyue_interaction`——只备主库会漏掉点赞 / 评论 / 弹幕 / 通知。
- **不备 Redis**：它只存结果缓存、限流计数、转码预约信号量，以及认证类的邮箱验证码与「刷新令牌已用过」标记，都能由业务与数据库重建；把快照恢复回来反而会带出「幽灵预约」「过期限流键」这类脏状态。**两个必须如实说明的后果**：① 恢复后仍处于有效期内、且已被使用过的旧刷新令牌会重新变成「没人用过」，重放检测在它们自然过期前短暂失效；② 正在有效期的邮箱验证码一并丢失，用户需要重新获取。两者都只影响一次性的短期状态，不影响账号与内容数据。
- **上传目录必须一起备**：转码产物可以重转，**用户上传的源文件丢了就找不回来**；k8s 模式默认跳过，需显式给 `UPLOADS_POD_NAME`，别漏。

失败能被发现：口令不对、`mysqldump` 中途出错都会以非 0 退出码结束，且不会留下半截归档（先写 `.tmp` 再原子改名），配合 cron 的输出或监控即可告警。

恢复（归档里自带 `CREATE DATABASE IF NOT EXISTS` 与 `USE`，因此**不用**再指定库名、也不用先建库）：

```bash
gunzip -c mysql/chuangyue-20261016-030000.sql.gz \
  | docker exec -i -e MYSQL_PWD="$MYSQL_PASSWORD" chuangyue-mysql mysql -uroot

# 上传目录：解到部署时挂载的那个宿主目录
docker run --rm -v "$PWD:/restore" alpine tar -xz -f /restore/uploads-20261016-030000.tar.gz -C /restore
```

> **诚实边界（恢复演练已补做）**：备份路径已在真实 MySQL 上跑通（两个库归档 `gzip -t` 校验通过、表数分别 14 / 9、失败路径退出码为 1 且无残留文件）；
> **恢复演练**：把归档灌进一个全新的 `mysql:8.0` 干净实例，逐库核对「表数 + 各表 `COUNT(*)` 合计」——`chuangyue` 14 表 / 63 行、`chuangyue_interaction` 9 表 / 48 行，与源库完全一致；上传目录归档 `tar -tzf` 校验通过。
> 需要说明：演练只在**本机容器**做过；上线前建议在**目标环境的独立实例**上再核一次（不同机器的权限 / 版本差异可能暴露这里没出现的问题）。

> 归档必须**再搬走一份**（rsync / ossutil 到对象存储）：备份与数据躺在同一块盘上，盘坏时两者一起没。

## 约定

- 镜像统一多阶段构建，最终镜像基于 `alpine` 或 `distroless`。
- 配置通过 ConfigMap、敏感信息通过 Secret 注入，不写死在镜像里。
- 节点规划与分阶段部署清单见本地 `docs/部署计划.md`——按约定该目录不入库。
