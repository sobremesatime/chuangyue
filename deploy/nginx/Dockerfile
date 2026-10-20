# 窗月 · 前端接入层镜像（Nginx 静态托管 + 反向代理网关）
#
# 两阶段：Node 构建前端产物 → Nginx 托管产物并把 /api、/uploads 反代到网关。
#
# 构建（在**仓库根目录**执行，因为要拷 frontend/ 与 deploy/nginx/）：
#   docker build -f deploy/nginx/Dockerfile -t chuangyue/web:1.0.0 .
#
# 说明：nginx.conf 打进镜像（不靠挂载），部署简单、产物不可变；
# 改配置时重建镜像即可。compose 本地开发若想改完即生效，可直接挂载
# deploy/nginx/nginx.conf 覆盖 /etc/nginx/nginx.conf。

# ---------------- 构建阶段：前端产物 ----------------
FROM node:20-alpine AS frontend

# 国内网络下 npm 官方源较慢，可用 --build-arg NPM_REGISTRY=... 覆盖
ARG NPM_REGISTRY=https://registry.npmmirror.com

WORKDIR /src
# 先只拷依赖清单：npm ci 的层可被缓存，改业务代码时不必重装依赖
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --registry=${NPM_REGISTRY}

COPY frontend/ ./
# build 脚本含 vue-tsc 类型检查（类型不过即构建失败，等于把契约校验前移到镜像构建）
RUN npm run build

# ---------------- 运行阶段：Nginx ----------------
FROM nginx:1.27-alpine

# 前端静态产物
COPY --from=frontend /src/dist /usr/share/nginx/html
# 接入层配置（完整 nginx.conf，覆盖镜像自带的）
COPY deploy/nginx/nginx.conf /etc/nginx/nginx.conf

EXPOSE 80
# 沿用官方镜像自带的 CMD ["nginx", "-g", "daemon off;"]
