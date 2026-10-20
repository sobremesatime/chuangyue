# 窗月 · 服务镜像（多阶段构建）
#
# 所有服务共用一个 Dockerfile，用 build arg SERVICE 选择要构建的 cmd 子目录：
#   docker build -f deploy/docker/Dockerfile --build-arg SERVICE=user -t chuangyue/user .
#
# 由 deploy/compose/docker-compose.yml 编排时无需手工构建：compose 会为每个服务传入 SERVICE。
# 配置不在镜像里（见 .dockerignore 排除了 configs），运行时经 compose 挂载注入。

# ---------------- 构建阶段 ----------------
FROM golang:1.26-alpine AS builder

# 模块代理：默认的 proxy.golang.org 在部分网络下不可达，构建时用国内镜像；
# 海外环境可 --build-arg GOPROXY=https://proxy.golang.org,direct 覆盖。
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src

# 先只拷依赖清单：go mod download 的层可被缓存，改代码时不必重下依赖。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# SERVICE 必填：漏传时立刻失败，避免打出个空镜像。
ARG SERVICE
RUN test -n "$SERVICE" || (echo "缺少 build arg SERVICE" >&2; exit 1)
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}

# ---------------- 运行阶段 ----------------
FROM alpine:3.20

# tzdata：created_at 等时间按容器时区序列化（DSN 用 loc=Local），必须保证时区数据可用。
# ca-certificates：供将来访问 https 外部依赖使用。
RUN apk add --no-cache ca-certificates tzdata

# 转码 Worker 需要 FFmpeg 产出 HLS；其余服务不装，避免镜像无谓膨胀。
ARG SERVICE
RUN if [ "$SERVICE" = "transcode-worker" ]; then apk add --no-cache ffmpeg; fi

WORKDIR /app
COPY --from=builder /out/app /app/app

# 与 compose 里的 TZ 保持一致，避免宿主机与容器口径漂移。
ENV TZ=Asia/Shanghai

ENTRYPOINT ["/app/app"]
