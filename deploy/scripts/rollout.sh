#!/usr/bin/env bash
#
# 窗月 · 镜像发布后的部署（滚动更新）脚本
#
# 为什么需要它：CI（.github/workflows/release.yml）只负责构建并推送 8 个镜像到 GHCR——
# 它连不到你的集群（CI 里不保存集群凭据）。镜像推上去之后，把集群切到新版本这一步，
# 过去只能人肉照 deploy/k8s/README.md 的命令逐条敲，既慢又容易漏掉某个服务。
# 本脚本把这一步固定下来：给一个标签，它把 8 个 Deployment 切过去并等它们就绪。
#
# 用法（需要你已获授权的 kubectl 访问；脚本自身不申请任何权限）：
#   ./rollout.sh 1.0.0                          # 全部服务切到 ghcr.io/<owner>/chuangyue/*:1.0.0
#   IMAGE_TAG=sha-1a2b3c4 ./rollout.sh          # 回滚 / 灰度常用 sha- 标签，也可用环境变量给
#   ./rollout.sh dev                            # dev 分支的镜像标签
#   SERVICES="video gateway" ./rollout.sh 1.0.0 # 只更新指定服务（留空 = 全部 8 个）
#   DRY_RUN=1 ./rollout.sh 1.0.0                # 只打印将执行的命令，不碰集群
#
# 环境变量（均有默认值，按需覆盖）：
#   IMAGE_TAG         镜像标签（也可作为第一个位置参数传入）   默认 latest
#   IMAGE_REGISTRY    镜像仓库前缀                            默认 ghcr.io/sobremesatime/chuangyue
#   K8S_NAMESPACE     命名空间                                默认 chuangyue
#   SERVICES          只更新的服务（空格分隔）                  默认空 = 全部
#   ROLLOUT_TIMEOUT   单个 Deployment 等待就绪的上限            默认 300s
#   DRY_RUN           非空则只打印、不执行
#
# 三点刻意的取舍：
# - **用 `kubectl set image` 而不是「改 overlay 再 apply」**：这一步的语义是「发布一个已经推上去的
#   镜像」，命令式改 Deployment 的 Pod 模板最直接，且不改动仓库里被跟踪的 kustomization.yaml。
#   代价是它与 overlay 里 `newTag` 的声明状态会分叉——下次 `kubectl apply -k` 会用 overlay 的标签
#   把它盖回去。要**固化**到某个版本，请把 overlay 的 `newTag` 一并改掉并提交（见 deploy/k8s/README.md）。
# - **先全部 set image，再统一等就绪**：让各服务的滚动并行推进，而不是「改一个等一个」把整体发布
#   拖成串行。任一服务在 ROLLOUT_TIMEOUT 内没就绪，脚本以非 0 退出，CI / 运维据此判成败。
# - **顺序把接入层 web 放在最后**：先在集群内部把后端换好，最后再切 web，缩短
#   「新前端打旧后端」这段错配窗口。
#
set -euo pipefail

IMAGE_TAG="${IMAGE_TAG:-latest}"
IMAGE_REGISTRY="${IMAGE_REGISTRY:-ghcr.io/sobremesatime/chuangyue}"
K8S_NAMESPACE="${K8S_NAMESPACE:-chuangyue}"
SERVICES="${SERVICES:-}"
ROLLOUT_TIMEOUT="${ROLLOUT_TIMEOUT:-300s}"
DRY_RUN="${DRY_RUN:-}"

# 位置参数优先于环境变量：`./rollout.sh 1.0.0` 比 `IMAGE_TAG=1.0.0 ./rollout.sh` 更顺手
if [[ $# -gt 0 ]]; then
  IMAGE_TAG="$1"
fi

command -v kubectl >/dev/null || {
  echo "需要 kubectl（且你已有相应集群访问授权）" >&2
  exit 1
}

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
# DRY_RUN 下只回显命令：这个动作会改生产，先让人看清要动哪些对象再执行
run() {
  if [[ -n "$DRY_RUN" ]]; then
    echo "+ $*"
  else
    "$@"
  fi
}

# 与 base 编排的 8 个进程一一对应；Deployment 名与容器名都等于服务名（见 deploy/k8s/base/*.yaml），
# 因此 set image 可以直接用 `<svc>=<镜像>` 的写法。web 放最后，理由见文件头。
ALL_SERVICES=(gateway user video interaction feed search transcode-worker web)
if [[ -n "$SERVICES" ]]; then
  # 允许用空格分隔覆盖成子集；这里是刻意不加引号做词拆分
  # shellcheck disable=SC2206
  TARGETS=($SERVICES)
else
  TARGETS=("${ALL_SERVICES[@]}")
fi

log "命名空间 $K8S_NAMESPACE；镜像 $IMAGE_REGISTRY/<service>:$IMAGE_TAG；目标：${TARGETS[*]}"

# 第一步：改期望状态（只提交，不等）
for svc in "${TARGETS[@]}"; do
  run kubectl -n "$K8S_NAMESPACE" set image "deployment/$svc" "$svc=$IMAGE_REGISTRY/$svc:$IMAGE_TAG"
done

# 第二步：等各服务的滚动更新真正就绪（老 Pod 全部退出、新 Pod Ready）
for svc in "${TARGETS[@]}"; do
  run kubectl -n "$K8S_NAMESPACE" rollout status "deployment/$svc" --timeout="$ROLLOUT_TIMEOUT"
done

log "完成"
if [[ -n "$DRY_RUN" ]]; then
  log "（DRY_RUN：以上命令均未真正执行）"
fi
