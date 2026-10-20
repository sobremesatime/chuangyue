# deploy/nginx · 接入层

全站唯一对外的入口：**Nginx 托管前端静态产物，并把 `/api`、`/uploads` 反向代理到网关**。
前端与接口同域，因此不需要 CORS；Refresh Token 的 `HttpOnly + Secure` Cookie 也能正常带上。

```
deploy/nginx/
├── nginx.conf          # 完整可替换的 nginx.conf（含 events / http / server）
├── Dockerfile          # 两阶段：Node 构建前端产物 → Nginx 托管产物并反代到网关
├── chuangyue.d/        # 可选（http 上下文）：HTTP→HTTPS 跳转等，示例为 *.conf.example
└── chuangyue-tls.d/    # 可选（server 上下文）：listen 443 与证书路径
```

## 构建镜像

在**仓库根目录**执行（要拷 `frontend/` 与 `deploy/nginx/`）：

```bash
docker build -f deploy/nginx/Dockerfile -t chuangyue/web:1.0.0 .
```

镜像里已经包含前端产物与 `nginx.conf`，部署时**不需要**再挂配置或静态目录。
K8s 侧的部署见 [`../k8s/base/web.yaml`](../k8s/base/web.yaml)。

## 复用同一份 nginx.conf

`nginx.conf` 是完整的配置文件，两种用法都支持：

- **容器（推荐）**：如上，打进镜像；
- **宿主机**：直接放到 `/etc/nginx/nginx.conf`（阿里云方案里 node-2 的用法）；
- **compose 本地开发**：若想在本地也走 Nginx，加一个 nginx 服务挂载本文件即可
  （上游都按服务名 `gateway:8080` 寻址，与 compose 的服务名一致）：
  ```yaml
  web:
    image: nginx:1.27-alpine
    volumes:
      - ../nginx/nginx.conf:/etc/nginx/nginx.conf:ro
      - ../../frontend/dist:/usr/share/nginx/html:ro
    ports: ["8080:80"]
  ```
  注意此时要把网关的 `8080` 让出来（compose 里网关是宿主机端口映射）。

## 启用 HTTPS（可选）

TLS 是**可选**的：主配置只在 http 与 server 两处留了 include 口子
（`/etc/nginx/chuangyue.d/*.conf`、`/etc/nginx/chuangyue-tls.d/*.conf`），目录为空时
行为与纯 HTTP 完全一致——同一份 `nginx.conf` 因此能同时供镜像、宿主机与本地 compose 复用
（本地没有证书，把 `listen 443` 与证书路径写死会让本地直接起不来）。证书路径与监听写在外部片段里，
location / 反代 / 缓存策略等业务配置仍只有一份，不会两处漂移。

步骤：

```bash
# 1) 申请证书（阿里云免费证书 或 acme.sh；DNS-01 不占用 80 端口，最省事）
#    acme.sh 示例：--issue -d <域名> --dns dns_ali 后 install-cert 到下面的路径
#    证书落到 /etc/nginx/certs/<域名>/{fullchain.pem,privkey.pem}

# 2) 启用片段（去掉 .example，并改成真实域名 / 证书路径）
cp chuangyue.d/10-https-redirect.conf.example      /etc/nginx/chuangyue.d/10-https-redirect.conf
cp chuangyue-tls.d/tls-listen.conf.example         /etc/nginx/chuangyue-tls.d/tls-listen.conf

# 3) 校验并热加载（校验证书路径与语法，不用重启）
nginx -t && nginx -s reload
```

容器部署则把这两个目录挂到同一路径即可：

```yaml
volumes:
  - ../nginx/nginx.conf:/etc/nginx/nginx.conf:ro
  - ../nginx/chuangyue.d:/etc/nginx/chuangyue.d:ro
  - ../nginx/chuangyue-tls.d:/etc/nginx/chuangyue-tls.d:ro
  - /etc/nginx/certs:/etc/nginx/certs:ro
```

两个已在实机上踩实的坑（都写进注释了，这里再点一次）：

- **include 的位置决定谁是 80 端口的默认 server**。nginx 把「同一端口上第一个出现的 server」
  当作默认 server，Host 匹配不上任何 `server_name` 时（例如直接用 IP 访问）就落到它。
  跳转块若排在主 server 之前，IP 直访也会被 301 掉；主配置特意把该 include 放在主 server **之后**，
  于是域名跳转、IP 直访（不被跳转）两不误。
- **`add_header` 不继承**。`/assets/` 与 `= /index.html` 自带 `add_header`（缓存策略），会覆盖掉
  server 级的 HSTS——而 `location /` 经 `try_files` 内部回退到的正是 `index.html`，也就是说
  浏览器首次落地的那个页面恰恰拿不到 HSTS。因此这两处显式重复了一份；
  浏览器按 RFC 6797 会忽略纯 HTTP 响应里的 HSTS，所以这份重复对本地无 TLS 场景无副作用。

启用后可按这几条探针验收（自签证书加 `-k`）：

```bash
curl -k -s -o /dev/null -w '%{http_code}\n' https://<域名>/healthz          # 期望 200
curl -k -s -I https://<域名>/ | grep -i strict-transport-security           # 期望有 HSTS
curl -s -I -H 'Host: <域名>' http://<IP>/                                   # 期望 301 到 https
curl -s -I -H 'Host: <IP>'   http://<IP>/                                   # 期望 200（IP 直访不跳转）
```

> 裸奔风险提示：不启用 TLS 时，JWT 与 Refresh Token 走明文——演示环境若绑域名对外，请务必开启。

## 关键配置点

| 配置 | 为什么 |
|---|---|
| `location / { try_files $uri $uri/ /index.html; }` | 前端是 **history 路由**（`createWebHistory`）；不配这条，刷新子路由会 404 |
| `client_max_body_size 2048m` | 与 `upload.max_size_mb` 对齐；默认 1MB 会让大文件在 Nginx 层直接 413 |
| `proxy_request_buffering off` | 上传直通，不在 Nginx 落盘缓冲，省一次全量写盘 |
| `proxy_read_timeout 600s` | 适配大文件上传与 HLS 分片的长连接 |
| `location /assets/` + `immutable` | Vite 产物带 hash，可长期强缓存；`index.html` 设 `no-store` 避免发版后命中旧入口 |
| `proxy_set_header X-Request-ID $http_x_request_id` | 透传链路 ID；空值时网关自行生成（`pkg/middleware/trace.go`），日志按同一 ID 串联 |
| `upstream gateway { keepalive 32; }` | 与网关复用长连接，减少握手开销 |
| `include /etc/nginx/chuangyue.d/*.conf`（放在主 server **之后**） | 端口默认 server 取「该端口第一个出现的 server」；跳转块若排在前面，会把 IP 直访也一起 301 走 |
| `add_header Strict-Transport-Security` 在 `/assets/`、`= /index.html` 各重复一份 | nginx 的 `add_header` **不继承**：location 自带 add_header 后 server 级那行会失效，而入口页恰是浏览器首次落地处 |
| `location = /healthz` | 供 K8s 探针 / 负载均衡健康检查 |

> ⚠️ **nginx 只在启动时解析一次 upstream 域名**：`gateway` 的服务名必须已存在，
> 否则 nginx 启动失败。K8s 的 `web` Pod 因此带了一个等网关 DNS 的 `initContainer`。
