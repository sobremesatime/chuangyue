# 窗月 · Kubernetes 清单

用 **Kustomize** 编排应用服务（`base` + `overlays`），与 `deploy/compose` 是同一套服务的两种落地方式。

```
deploy/k8s/
├── base/                     # 所有环境共用的应用清单
│   ├── kustomization.yaml
│   ├── namespace.yaml        # Namespace chuangyue
│   ├── config.yaml           # 应用配置（生成 ConfigMap chuangyue-config）
│   ├── secret.yaml.example   # 敏感配置模板（**不可 apply**，只作键名对照）
│   ├── pvc-uploads.yaml      # 上传/转码产物共享卷（RWX）
│   ├── web.yaml              # 接入层（Nginx）：前端静态 + /api 反代，**唯一对外入口**
│   ├── gateway.yaml          # 集群内的 HTTP↔gRPC 翻译层（不再对外暴露）
│   ├── user.yaml             # :9103
│   ├── video.yaml            # :9102
│   ├── interaction.yaml      # :9101
│   ├── feed.yaml             # :9104
│   ├── search.yaml           # :9105
│   └── transcode-worker.yaml # 无端口，消费 Kafka
├── infra/                    # 自建中间件（MySQL / Redis / Etcd / Kafka / MinIO，全部自建）
│   ├── kustomization.yaml
│   └── mysql.yaml / redis.yaml / etcd.yaml / kafka.yaml / minio.yaml
├── backup/                   # 可选：集群内定时备份（CronJob + 归档 PVC + 备份脚本）
└── overlays/
    ├── dev/                  # 单副本、tag=dev、沿用 base 配置
    └── prod/                 # 1 副本、tag=1.0.1、prod 配置、资源抬高、Ingress(→web)、
                              # local-storage.yaml（k3s 只有 local-path 时的共享卷适配）
```

接入层的镜像与配置在 `deploy/nginx/`（`nginx.conf` + `Dockerfile`），不属于本目录。

## 入口链路

```
公网 → Ingress(prod) ─┐
                      └→ Service web(80) → Nginx Pod ─┬→ 静态文件（前端 dist）
                                                     ├→ /api/     → Service gateway:8080 → 各微服务
                                                     └→ /uploads/ → Service gateway:8080（静态托管）
```

- **对外只有 `web`**：前端与接口同域，前端用相对路径 `/api/v1`，因此不需要 CORS，
  Refresh Token 的 `HttpOnly + Secure` Cookie 也能正常带上（前提是走 HTTPS）。
- **网关不再对外暴露**：它仍是 ClusterIP，只接受 `web` 的转发，避免绕过接入层。
- dev overlay 不带 Ingress，用 `kubectl -n chuangyue port-forward svc/web 8080:80` 访问。

## 范围：应用清单 + 可选的中间件清单

`base` + `overlays` 只编排 **8 个应用进程**（web / gateway / 五个服务 / 转码 Worker）。
**MySQL / Redis / Etcd / Kafka / MinIO / Elasticsearch 不在 base 里，全部自建**：
`kubectl apply -k deploy/k8s/infra` 起一整套单实例中间件
（详见该目录的 `kustomization.yaml` 文件头，含「单实例接受哪些代价」的说明）。
其中 **Etcd 是服务注册与发现的中心**，缺了它各服务注册不上、网关发现不到后端，必须最先确保就绪。

集群内必须存在同名 Service（名字与 `base/config.yaml` 里的 `host` 一致）：

| 依赖 | 集群内 Service 名 | 用途 |
|---|---|---|
| MySQL | `mysql` | `chuangyue` 库（用户 / 视频）+ `chuangyue_interaction` 库（互动） |
| Redis | `redis` | 验证码、令牌、推荐缓存、限流计数、刷新令牌重放标记 |
| Etcd | `etcd` | 服务注册与发现（**必需**，自建）——缺了它各服务注册不上、网关发现不到后端 |
| Kafka | `kafka` | **功能必需**：转码任务、互动计数回写、视频索引事件（producer 惰性连接，缺了不崩但会静默不转码、索引滞后） |
| MinIO | `minio` | 仅在 `upload.driver=minio` 时需要 |
| Elasticsearch | `elasticsearch` | 仅在 `search.es.enabled=true` 时需要；不可用时检索退回库内 LIKE |

> 依赖部署在别的命名空间时，请把配置里的主机名写成 FQDN（如 `mysql.infra.svc.cluster.local`）。

> **Elasticsearch 的镜像来源**：官方镜像位于 `docker.elastic.co`，国内没有可用的公共代理。
> 清单统一使用 DaoCloud 镜像站的同一镜像（digest 一致）：
> `elastic.m.daocloud.io/elasticsearch/elasticsearch:8.15.3`，配合 `imagePullPolicy: IfNotPresent`。
> **部署到新节点前需先预置镜像**，否则 Pod 会停在 `Pulling`：
>
> ```bash
> sudo k3s ctr images pull --platform linux/amd64 \
>   elastic.m.daocloud.io/elasticsearch/elasticsearch:8.15.3
> ```
>
> 需要使用自有仓库时，可将同一镜像同步至 TCR 等私有 registry，并同步修改
> `infra/elasticsearch.yaml` 的 `image` 字段；`--platform linux/amd64` 不能省略，
> 否则可能拉取到多架构索引导致运行时无法解析。

数据库结构由 `migrations/` 提供，**不会自动执行**（k8s 没有 compose 的 initdb 机制），
部署时必须手工跑一次——见下文「初始化数据库」。

## 构建与发布镜像

### CI 自动发布（推荐）

`.github/workflows/release.yml` 在 **main 分支推送**或 **`v*.*.*` 标签**上，用矩阵构建并推送 8 个镜像到 GHCR：

- `main` → 标签 `dev`（dev overlay 用的就是这个名字）
- `v1.0.1` → 标签 `1.0.1` + `latest`（prod overlay 当前使用的是 `1.0.1`）
- 每个提交另附 `sha-xxxxxxx` 标签，便于回滚追溯

镜像地址为 `ghcr.io/<owner>/chuangyue/<service>`，凭据只用 Actions 内置的 `GITHUB_TOKEN`
（工作流声明 `permissions: packages: write`），**无需配置额外 Secret**。
两个 overlay 的 `images:` 均已通过 `newName` 指向该地址。

> **拉取权限**：GHCR 包默认私有，集群需先用 PAT `docker login ghcr.io` 生成 `imagePullSecret`，
> base 已在 8 个业务 Deployment 里声明了 `imagePullSecrets: [{name: ghcr-auth}]`；你只需保证该 Secret 存在，
> 否则 Pod 会 `ImagePullBackOff`（把 8 个 GHCR 包设为 public，就完全不需要这个 Secret）。

`ci.yml` 里另有一项**零凭据**的清单校验：用 `kubectl kustomize` 离线渲染 dev / prod / infra / backup 四套，
不连集群、不取密钥，渲染通过即说明 base + overlay 结构、patch 与镜像引用自洽。

### 本地手工构建（可选，不上 CI 时用）

后端 7 个服务共用一个 `deploy/docker/Dockerfile`，用 `SERVICE` 选择 cmd：

```bash
TAG=1.0.0
for svc in gateway user video interaction feed search transcode-worker; do
  docker build -f deploy/docker/Dockerfile --build-arg SERVICE=$svc \
    -t ghcr.io/<owner>/chuangyue/$svc:$TAG .
  docker push ghcr.io/<owner>/chuangyue/$svc:$TAG
done
```

接入层 `web` 用**另一个** Dockerfile（Node 构建前端 → Nginx 托管，配置已打进镜像）：

```bash
docker build -f deploy/nginx/Dockerfile -t ghcr.io/<owner>/chuangyue/web:$TAG .
docker push ghcr.io/<owner>/chuangyue/web:$TAG
```

> 想把镜像放到别的仓库时，改各 overlay 的 `images:` `newName` 即可（如 `registry.example.com/chuangyue/web`）。

## 部署（首次按顺序执行）

顺序不能乱：**命名空间 → Secret → 中间件 → 建表 → 应用**。每一步失败都能在 `kubectl -n chuangyue get pods`
里直接看到原因，不会带着错配置跑起来。

```bash
# -1) 命名空间必须先存在：下面的 Secret 都建在这个 namespace 里
kubectl apply -f deploy/k8s/base/namespace.yaml

# 0) 私有镜像仓库的拉取凭据（GHCR 包默认私有；已设为 public 或换别的仓库可跳过）
kubectl -n chuangyue create secret docker-registry ghcr-auth \
  --docker-server=ghcr.io --docker-username=<github 用户名> --docker-password=<PAT>

# 1) 敏感配置：必须先用真实值建出来（仓库里只有不可 apply 的模板）
kubectl -n chuangyue create secret generic chuangyue-secrets \
  --from-literal=CHUANGYUE_MYSQL_PASSWORD='<MySQL root 口令>' \
  --from-literal=CHUANGYUE_INTERACTION_MYSQL_PASSWORD='chy_interaction_pwd' \
  --from-literal=CHUANGYUE_REDIS_PASSWORD="$(openssl rand -base64 24)" \
  --from-literal=CHUANGYUE_JWT_ACCESS_SECRET="$(openssl rand -base64 48)" \
  --from-literal=CHUANGYUE_JWT_REFRESH_SECRET="$(openssl rand -base64 48)" \
  --from-literal=CHUANGYUE_UPLOAD_MINIO_ACCESS_KEY="$(openssl rand -hex 12)" \
  --from-literal=CHUANGYUE_UPLOAD_MINIO_SECRET_KEY="$(openssl rand -base64 32)" \
  --from-literal=CHUANGYUE_MAIL_PASSWORD='<服务商 API Key>'

# 2) 中间件（全部自建）
kubectl apply -k deploy/k8s/infra
kubectl -n chuangyue get pods -w        # 等 mysql / redis / etcd / kafka / minio 全部 Ready

# 3) 建表（详见下文「初始化数据库」）
# 4) 应用
kubectl apply -k deploy/k8s/overlays/prod     # 或 overlays/dev
kubectl -n chuangyue get pods -w
```

> **也可用一键脚本**：`deploy/scripts/bootstrap-k8s.sh` 把上面第 -1 ～ 3 步（命名空间 → Secret → 中间件 → 灌库）
> 按正确顺序串起来，带 `--dry-run` 与逐步确认，并会在建 Secret 时自动生成 JWT / Redis / MinIO 随机密钥。
> 用法见该脚本头部注释（口令只走环境变量）。
>
> 部署完成后若要**滚动更新**（镜像已由 CI 推到 GHCR）：用 `deploy/scripts/rollout.sh`，见下文「发布与滚动更新」。
>
> **为什么 Secret 不在 kustomization 里**：把占位值（`change-me-*`）留在可 apply 的清单里，
> 迟早会有人直接 `apply` 上去，于是生产跑着默认 JWT 密钥——等于任何人都能伪造管理员令牌。
> 现在 Secret 缺失时 Pod 会停在 `CreateContainerConfigError`，属于一眼可见的失败。

dev overlay 无 Ingress，用本地转发访问（`web` 是接入层，前端与 `/api` 都从它进）：

```bash
kubectl -n chuangyue port-forward svc/web 8080:80
# 浏览器打开 http://127.0.0.1:8080
```

本地不依赖真实镜像、只想看渲染结果：

```bash
kubectl kustomize deploy/k8s/overlays/prod
```

## 发布与滚动更新

镜像由 CI（`.github/workflows/release.yml`）推到 GHCR 之后，用 `deploy/scripts/rollout.sh` 把集群切到新版本
——**CI 自身连不到集群**（不在流水线里保存集群凭据），这一步必须由持有 kubectl 授权的人执行：

```bash
./deploy/scripts/rollout.sh 1.0.0                             # 全部 8 个 Deployment 切到 :1.0.0 并等就绪
SERVICES="video gateway" ./deploy/scripts/rollout.sh 1.0.0    # 只更新部分服务
IMAGE_TAG=sha-1a2b3c4 ./deploy/scripts/rollout.sh             # 用 sha- 标签回滚 / 灰度
DRY_RUN=1 ./deploy/scripts/rollout.sh 1.0.0                   # 只打印将执行的命令，不碰集群
```

它用 `kubectl set image` 逐个改 Deployment 的 Pod 模板，再 `kubectl rollout status` 等各服务滚动完成；
**顺序把接入层 `web` 放在最后**，缩短「新前端打旧后端」的错配窗口。任一服务未在 `ROLLOUT_TIMEOUT`（默认
300s）内就绪，脚本以非 0 退出。

取舍：命令式 set image **不改动仓库里被跟踪的 kustomization.yaml**，代价是与 overlay 里 `newTag` 的声明
状态会分叉——下次 `kubectl apply -k` 会用 overlay 的标签把它盖回去。**要固化的版本，请把 overlay 的
`newTag` 一并改掉并提交**，再 `kubectl apply -k`；`rollout.sh` 更适合「发布一个刚推上去的镜像」或
「用 `sha-` 标签回滚」这种一次性动作。

## 集群内定时备份（可选）

自建 MySQL 没有自动备份。仓库里有两条通道，各管一种场景，**不混用**（半截结果最难排查）：

- `deploy/scripts/backup.sh`：**宿主机侧**，跑在能执行 `docker` / `kubectl` 的机器上（可进容器、可 exec Pod）。
- `deploy/k8s/backup/`：**集群内**，备份 Pod 自己就挂在 MySQL 与上传卷旁边，不需要 docker / kubectl。

用后者（纯集群、不进宿主）：

```bash
kubectl apply -k deploy/k8s/backup
kubectl -n chuangyue get jobs -w                                      # 看历史执行
kubectl -n chuangyue create job --from=cronjob/chuangyue-backup backup-manual   # 立刻手工跑一次
```

一个 CronJob 每天 03:30 在集群内把两个业务库（`chuangyue` + `chuangyue_interaction`）与上传卷打成归档，
写进独立 PVC `chuangyue-backups`，并按保留期（默认 7 天）清理旧归档。几点说明：

- 备份 Pod 用公共镜像 `mysql:8.0`（自带 `mysqldump` / `gzip` / `tar` 且脚本启动自检），
  因此**不需要 imagePullSecret**；root 口令取自 Secret `chuangyue-secrets` 的 `CHUANGYUE_MYSQL_PASSWORD`。
- 它要挂上传卷（RWO），所以带 `nodeSelector: chuangyue.io/storage=true`，与业务 Pod 落在同一节点；
  用真正的 RWX 存储时删掉该项即可。
- **它不负责异地化**：归档与数据同盘，盘坏一起没——仍需另配一步 rsync / ossutil 搬到对象存储。

> 自建 MySQL 没有自动备份，`backup/` 这个 CronJob 是兜底手段，**建议保留**。

## 初始化数据库

k8s 里没有 compose 的 `/docker-entrypoint-initdb.d` 机制，`migrations/` **不会被自动执行**。

**全新库**：把仓库的 `migrations/` 拷到能执行 `kubectl` 的机器上，按文件名顺序逐个灌入
（`0015_split_interaction_db.sql` 会建互动库与独立账号，必须执行）：

```bash
for f in migrations/*.sql; do
  echo "==> $f"
  kubectl -n chuangyue exec -i mysql-0 -- \
    mysql -uroot -p"$CHUANGYUE_MYSQL_PASSWORD" chuangyue < "$f"
  # ↑ 必须带库名 chuangyue：migrations/0001~0016 都是「无库名前缀」的 CREATE/ALTER TABLE，
  #   不选库会报 ERROR 1046 (3D000): No database selected。
done
```

**存量库升级**：只执行还没跑过的文件。当前最新是 `0016_add_user_token_version.sql`
（新增 `user.token_version`，重置密码与刷新令牌重放吊销都依赖它）；它**不幂等**，重复执行会报
`Duplicate column name`，先确认再跑：

```bash
kubectl -n chuangyue exec -i mysql-0 -- \
  mysql -uroot -p"$CHUANGYUE_MYSQL_PASSWORD" chuangyue \
  -e "SHOW COLUMNS FROM user LIKE 'token_version';"      # 有输出就说明已经跑过

kubectl -n chuangyue exec -i mysql-0 -- \
  mysql -uroot -p"$CHUANGYUE_MYSQL_PASSWORD" chuangyue < migrations/0016_add_user_token_version.sql
# ↑ 0016 是未带库名的 ALTER TABLE user，同样必须指定 chuangyue 库。
```

> 为什么没有迁移工具：项目只有 16 个一次性脚本、且要兼容「本地 compose 空库自动初始化」，
> 引入 golang-migrate 之类的框架收益不大。代价就是升级时必须人工判断哪些已执行——
> 新增迁移时请继续保持「文件头写清是否幂等」的约定（`0015` 幂等，`0016` 不幂等）。

管理员账号仍按 `migrations/README.md` 的方式创建：先注册，再 `UPDATE user SET role=1`。

## 配置与密钥

- **配置**：`base/config.yaml` 经 `configMapGenerator` 生成 ConfigMap `chuangyue-config`，
  各 Pod 以 `subPath` 挂载为 `/app/config.yaml`（服务启动参数 `-c /app/config.yaml`）。
  prod overlay 用 `behavior: merge` 覆盖同名 key。
- **密钥**：`secret.yaml.example` 只是**键名对照模板，不要 apply**（也不在 kustomization 里）。
  真实 Secret 用上面的 `kubectl create secret` 建，或交给 SealedSecrets / External Secrets / SOPS
  把密文提交进 Git。键名即环境变量名（`CHUANGYUE_` 前缀），Pod 用 `envFrom` 全量注入，
  配置文件里对应键留空由环境变量覆盖（规则见 `pkg/config`）。
- **三个不能随手生成的值**（其余都该用 `openssl rand`）：
  - `CHUANGYUE_MYSQL_PASSWORD`：`infra/mysql.yaml` 直接把它当 `MYSQL_ROOT_PASSWORD` 用，
    Secret 与 MySQL 两边天然一致。
  - `CHUANGYUE_INTERACTION_MYSQL_PASSWORD`：**必须等于 `migrations/0015` 里 `CREATE USER` 的口令**
    （默认 `chy_interaction_pwd`）。要换口令，得先改迁移文件再初始化，或在库里 `ALTER USER`。
  - `CHUANGYUE_MAIL_PASSWORD`：服务商签发的 API Key，只能抄。

## 关键设计点

- **服务注册地址用 Pod IP**：各服务以 `-advertise $(POD_IP):端口` 注册到 Etcd（`POD_IP` 由
  Downward API 注入），而不是 Service DNS——这样每个实例注册自身地址，调用方能做**实例级**
  负载均衡；填 Service 名会让所有实例注册成同一地址，把负载均衡推回 kube-proxy。
- **服务名固定**：base 里**不设 `namePrefix`**，因为 `config.yaml` 用服务名（`user` / `video` …）
  互相寻址，加前缀会导致发现与配置对不上。
- **共享卷：base 要 RWX，prod 在 k3s 上降级为「节点本地 + 同节点」**。`video`（写源文件/封面）、
  `transcode-worker`（读源、写 HLS）、`gateway`（静态托管 `/uploads`）挂同一个 PVC，
  它们都是不同 Pod，所以 base 的 `chuangyue-uploads` 声明为 `ReadWriteMany`，需要 NFS / CephFS / 云 NAS。
  **k3s 只有 local-path（RWO）**，因此 `overlays/prod/local-storage.yaml` 做两件事：
  把该 PVC 改成 `ReadWriteOnce` + `local-path`，并用 `nodeSelector: chuangyue.io/storage=true`
  把这几个 Deployment 钉在同一个节点上——RWO 的语义是「一个**节点**可写」，同节点上的多个 Pod 可以共享。
  代价是这些 Pod 没有跨节点容灾，且**必须给节点打标签**（否则一直 Pending）：
  `kubectl label node <node-2> chuangyue.io/storage=true`。
  有真正的 RWX 存储时删掉那个 patch 即可。
- **`upload.driver=minio` 能解决哪一半**：视频源文件与封面进桶，但 **HLS 产物仍写本地卷**
  （`transcode.output_dir`，由网关的 `/uploads` 静态托管），所以上面那条同节点约束不会因为切 MinIO 而消失。
- **头像已接入 `pkg/storage`**：`upload.driver=minio` 时写桶（key 前缀 `avatar/`，换头像会删旧对象），
  `local` 时仍写 `/app/uploads`。因此 **base 的 `user` 仍挂这份卷**（local 下 `gateway`
  是另一个进程，不共享就「上传成功、图片 404」）。
  **生产已切到 minio**：prod overlay 用一组 remove 补丁把 user 的 uploads 卷挂载去掉，
  但**保留 nodeSelector 钉在 node-2**（与 gateway 同机走回环，且该节点水位最宽松）。
  想让它彻底可漂移，删掉那条 nodeSelector 即可。
- **MinIO 的对外入口走 Ingress 路径，不用 NodePort**：`overlays/prod/minio-ingress.yaml`
  把 `/chuangyue` 前缀转发到 `minio-s3:9000`（只暴露 S3，**不含 Console 9001**）。
  好处是 `upload.minio.public_url` 填站点入口的 IP / 域名即可——MinIO Pod 落在哪台都不影响地址，
  也不必在防火墙额外放通 9000。该文件与 `infra/minio.yaml` **必须同时启用/停用**。
- **节点分配是刻意排过的**：node-3 只有 1.9Gi，装完 ES（requests 768Mi）后余量很小，
  因此 interaction / search 被改钉到 node-1；MinIO 钉在 node-2。
  详细核算见《部署计划》第 0 节。
- **探针**：接入层 / 网关用 HTTP `/healthz`；gRPC 服务用 `tcpSocket`；转码 Worker 无端口，不做端口探针，
  靠 `restartPolicy` 兜底（Kafka 未就绪时它会退出并由退避重启拉起，与 compose 行为一致）。
- **接入层先等网关**：`web` 的 Pod 带一个 `initContainer`（`nslookup gateway` 轮询）。
  原因是 **nginx 只在启动时解析一次 upstream 域名**，若 `web` 先于网关 Service 起来会因解析失败而 CrashLoop。
- **优雅退出**：`terminationGracePeriodSeconds: 30`（Worker 60），配合应用内的 GracefulStop，
  滚动更新/缩容时在途请求不会被硬切。
- **Ingress**（仅 prod，指向 `web`）：已按 **k3s 自带的 Traefik** 编写（`ingressClassName: traefik`），
  复用 Traefik 已占用的 80/443，不要再给 `web` 建 LoadBalancer（会与 Traefik 抢端口）。
  当前**不写 `host`**（catch-all），以便用节点 IP 直接验收；挂域名时补回 `host` 与 `tls`。
  上传体积无需像 nginx-ingress 那样配 `proxy-body-size`——Traefik 默认不限制请求体。
- **指标暴露**：各 Pod 的容器端口已带 `metrics`（`9201`~`9206`，由 `-metrics-addr` 指定），
  网关的 `/metrics` 与业务同端口（`8080`）。**K8s 侧监控属集群级能力，本仓库不编排 Prometheus / Grafana / Alertmanager**——
  用 kube-prometheus-stack 按 Pod 注解 / ServiceMonitor 发现上述端口即可；
  `deploy/monitoring` 那套只为本地 compose 服务。**但告警规则本身可复用**：`deploy/monitoring/prometheus/alerts.yml`
  里的表达式与环境无关，改写成 `PrometheusRule` CRD 即可（只需把 `job` / `service` 标签的取值对齐集群里的命名）。
  切勿把 Prometheus / Alertmanager / Grafana 或 `/metrics` 暴露到公网——Alertmanager 的 UI 能静默告警，等于把「关掉报警」的能力交出去。

## 与 Compose 的对照

| 维度 | Compose（deploy/compose） | K8s（本目录） |
|---|---|---|
| 定位 | 本地一键起全栈 / 开发 | 集群部署 |
| 基础设施 | 同一 compose 里起 mysql/redis/etcd/kafka/minio | 由 `infra/` 单独自建，不在 `base` 内 |
| 接入层 | 无（前端走 Vite proxy，网关直接暴露 `8080`） | `web`（Nginx）托管前端 + 反代 `/api`，网关仅在集群内 |
| 配置注入 | 挂载 `deploy/compose/config.yaml` | ConfigMap + Secret `envFrom` |
| 服务地址 | `-advertise <compose 服务名>:端口` | `-advertise $(POD_IP):端口` |
| 共享上传目录 | 命名卷 `uploads_data` | PVC `chuangyue-uploads`（RWX） |
| 副本与滚动 | 单实例、`restart: unless-stopped` | Deployment 副本 + 滚动更新 |

> 接入层的 `nginx.conf` 两种环境通用（上游都按服务名 `gateway:8080` 寻址），
> 若也想在 compose 里跑 Nginx，加一个挂载 `deploy/nginx/nginx.conf` 的 nginx 服务即可。
> Compose 里 Kafka 用双监听器（宿主机 `127.0.0.1:9092` / 容器 `kafka:9094`）；
> K8s 里不存在「宿主机客户端」这一侧，统一用集群内 `kafka:9092` 即可。
