#!/usr/bin/env bash
#
# 窗月 · k8s 首次初始化（把空集群推到「可部署应用」的状态）
#
# 为什么需要它：首次部署的顺序不能乱——「命名空间 → Secret → 中间件 → 灌库 → 应用」。
# 顺序错一步就踩坑：
#   - Secret 没建，MySQL 的 StatefulSet 取不到 CHUANGYUE_MYSQL_PASSWORD → CreateContainerConfigError；
#   - 库没灌，应用一连接就报表不存在。
# 本脚本把这几步按正确顺序串起来，并在每个「会造成不可逆结果」的动作前停下确认。
#
# 用法（口令只走环境变量，不进命令行、不进 shell history）：
#   MYSQL_PASSWORD='xxx' ./bootstrap-k8s.sh                     # 建 Secret + 灌库 + 验收
#   MYSQL_PASSWORD='xxx' ./bootstrap-k8s.sh --with-infra        # 顺带起中间件并等 MySQL 就绪
#   MYSQL_PASSWORD='xxx' ./bootstrap-k8s.sh --admin-user admin  # 灌库后把该账号提权为管理员
#   ./bootstrap-k8s.sh --dry-run                                # 只打印将执行的动作，不碰集群
#
# 环境变量：
#   MYSQL_PASSWORD                MySQL root 口令。**必填**；自建 infra 直接拿它当
#                                 MYSQL_ROOT_PASSWORD，所以与 Secret 天然一致。
#   INTERACTION_MYSQL_PASSWORD    互动库独立账号口令，默认 chy_interaction_pwd。
#                                 **必须等于 migrations/0015 里 CREATE USER 的口令**。
#   MAIL_PASSWORD                 服务商（Resend / 阿里云）API Key；留空 = dev 的 log 驱动。
#   REDIS_PASSWORD / JWT_ACCESS_SECRET / JWT_REFRESH_SECRET
#   MINIO_ACCESS_KEY / MINIO_SECRET_KEY
#                                 留空则自动 openssl rand 生成（只存进集群 Secret，不回显）。
#   GITHUB_USER / GITHUB_PAT      两者都给了才建 ghcr-auth（拉私有镜像的凭据）；
#                                 不提供则跳过（镜像已 public 时不需要）。
#   K8S_NAMESPACE                 命名空间，默认 chuangyue。
#   MYSQL_POD_NAME                MySQL Pod 名，默认 mysql-0。
#
# 开关：
#   --dry-run         只打印，不执行（连存在性检查也一律「假定不存在」，无需连集群即可预览）
#   --yes             不交互确认（供无人值守 / CI 用）
#   --with-infra      apply -k deploy/k8s/infra，并等 MySQL 就绪
#   --skip-secret     跳过建 Secret（Secret 已存在时）
#   --skip-db         跳过灌库
#   --force-secret    覆盖已存在的 Secret（**会重置 JWT / Redis / MinIO 密钥**，需确认）
#   --force-db        库非空也照灌（谨慎：0016 不幂等，会报 Duplicate column name）
#   --admin-user NAME 灌库后把 NAME 提权为管理员（该账号需已在站点注册）
#
# 三点刻意的取舍：
# - **口令只走环境变量**：写成 --password 参数会出现在 ps 与 shell history 里，也会被
#   误打进日志。脚本内部一律用 `env MYSQL_PWD=` 传给 mysql 客户端，连容器内 ps 都看不到。
# - **灌库前先探测库是否非空**：非空默认拒灌——migrations 里 0016 不幂等，重跑会报
#   Duplicate column name 并中断。存量库升级请照 deploy/k8s/README.md 的升级段手动跑。
# - **随机密钥不打印**：生成的 JWT / Redis / MinIO 口令只存进集群 Secret，脚本不回显，
#   避免落进终端日志；要留档请自行 `kubectl get secret -o yaml` 导出到本地记录文件。
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

K8S_NAMESPACE="${K8S_NAMESPACE:-chuangyue}"
MYSQL_POD_NAME="${MYSQL_POD_NAME:-mysql-0}"
SECRET_NAME="chuangyue-secrets"
INTERACTION_MYSQL_PASSWORD="${INTERACTION_MYSQL_PASSWORD:-chy_interaction_pwd}"
MAIL_PASSWORD="${MAIL_PASSWORD:-}"

DRY_RUN=0
ASSUME_YES=0
WITH_INFRA=0
SKIP_SECRET=0
SKIP_DB=0
FORCE_SECRET=0
FORCE_DB=0
ADMIN_USER=""

log()  { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
step() { printf '\n=== %s ===\n' "$*"; }

# 执行命令；dry-run 时只打印不执行。注意：**不要**用它传含口令的命令行（会回显）。
run() {
  log "+ $*"
  if [[ "$DRY_RUN" == 1 ]]; then return 0; fi
  "$@"
}

# 交互确认：--yes 直接通过；非交互（无 tty）且未加 --yes 则中止，避免误操作。
confirm() {
  local prompt="$1" ans
  if [[ "$ASSUME_YES" == 1 ]]; then return 0; fi
  if [[ ! -t 0 ]]; then
    log "$prompt → 非交互环境须加 --yes 才继续，已中止" >&2
    return 1
  fi
  read -r -p "$prompt [y/N] " ans || true
  if [[ "$ans" == "y" || "$ans" == "Y" ]]; then return 0; fi
  return 1
}

# 存在性检查：dry-run 时一律当作「不存在」，以便完整打印将要执行的动作（也无需连集群）。
ns_exists() { if [[ "$DRY_RUN" == 1 ]]; then return 1; fi; kubectl get namespace "$1" >/dev/null 2>&1; }
k_exists()  { if [[ "$DRY_RUN" == 1 ]]; then return 1; fi; kubectl -n "$K8S_NAMESPACE" get "$1" >/dev/null 2>&1; }

# 在 Pod 里跑一次 mysql（口令经 MYSQL_PWD 传入，不出现在命令行）。
# 用法：mysql_in_pod [库名] [mysql 参数…]；stdin 可直接重定向 SQL 文件进来。
mysql_in_pod() {
  local db="${1:-}"; shift || true
  if [[ -n "$db" ]]; then
    kubectl -n "$K8S_NAMESPACE" exec -i "$MYSQL_POD_NAME" -- \
      env MYSQL_PWD="$MYSQL_PASSWORD" mysql -uroot "$db" "$@"
  else
    kubectl -n "$K8S_NAMESPACE" exec -i "$MYSQL_POD_NAME" -- \
      env MYSQL_PWD="$MYSQL_PASSWORD" mysql -uroot "$@"
  fi
}

# ---------- 解析参数 ----------
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)      DRY_RUN=1 ;;
    --yes|-y)       ASSUME_YES=1 ;;
    --with-infra)   WITH_INFRA=1 ;;
    --skip-secret)  SKIP_SECRET=1 ;;
    --skip-db)      SKIP_DB=1 ;;
    --force-secret) FORCE_SECRET=1 ;;
    --force-db)     FORCE_DB=1 ;;
    --admin-user)   ADMIN_USER="${2:-}"; shift ;;
    -h|--help)      sed -n '2,41p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（-h 看用法）" >&2; exit 2 ;;
  esac
  shift
done

if [[ "$DRY_RUN" == 0 ]]; then
  command -v kubectl >/dev/null || { echo "需要 kubectl（且你已有相应集群访问授权）" >&2; exit 1; }
fi

[[ -n "${MYSQL_PASSWORD:-}" ]] || {
  echo "必须用环境变量提供 MYSQL_PASSWORD（不用命令行参数，避免口令出现在 ps / shell history 里）" >&2
  exit 1
}

if [[ "$DRY_RUN" == 1 ]]; then log "DRY_RUN：只打印将执行的动作，不碰集群"; fi

# ---------- 阶段 0 · 命名空间 ----------
step "0 · 命名空间 $K8S_NAMESPACE"
if ns_exists "$K8S_NAMESPACE"; then
  log "命名空间已存在，跳过"
else
  run kubectl apply -f "$REPO_ROOT/deploy/k8s/base/namespace.yaml"
fi

# ---------- 阶段 1 · 私有镜像拉取凭据（可选） ----------
step "1 · 镜像拉取凭据 ghcr-auth（可选）"
if [[ -n "${GITHUB_USER:-}" && -n "${GITHUB_PAT:-}" ]]; then
  if k_exists secret/ghcr-auth; then
    log "ghcr-auth 已存在，跳过（要更换凭据请先删除再重跑）"
  elif confirm "创建 ghcr-auth？"; then
    if [[ "$DRY_RUN" == 1 ]]; then
      log "+ kubectl -n $K8S_NAMESPACE create secret docker-registry ghcr-auth --docker-server=ghcr.io …（值不回显）"
    else
      kubectl -n "$K8S_NAMESPACE" create secret docker-registry ghcr-auth \
        --docker-server=ghcr.io --docker-username="$GITHUB_USER" --docker-password="$GITHUB_PAT"
      log "ghcr-auth 已创建"
    fi
  else
    log "已跳过 ghcr-auth"
  fi
else
  log "未提供 GITHUB_USER / GITHUB_PAT，跳过（GHCR 包默认私有；已设 public 或换仓库则无需此步）"
fi

# ---------- 阶段 2 · 敏感配置 Secret ----------
step "2 · 敏感配置 Secret $SECRET_NAME"
if [[ "$SKIP_SECRET" == 1 ]]; then
  log "--skip-secret：跳过"
elif k_exists "secret/$SECRET_NAME"; then
  if [[ "$FORCE_SECRET" == 1 ]]; then
    log "Secret 已存在，--force-secret：将删除重建（**会重置 JWT / Redis / MinIO 密钥，已签发令牌将失效**）"
    if confirm "确认删除并重建 $SECRET_NAME？"; then
      run kubectl -n "$K8S_NAMESPACE" delete secret "$SECRET_NAME"
    else
      log "已取消，保留原 Secret"
      SKIP_SECRET=1
    fi
  else
    log "Secret 已存在，跳过（要改值：--force-secret 重建，或 kubectl create … --dry-run=client -o yaml | kubectl apply -f -）"
    SKIP_SECRET=1
  fi
fi

if [[ "$SKIP_SECRET" == 0 ]]; then
  # 随机密钥：留空则自动生成（只存进集群，不回显）。生成前要求 openssl 可用。
  # dry-run 不真正生成（也就无需 openssl），只会在下面提示「将生成」。
  if [[ "$DRY_RUN" == 0 ]]; then
    need_random=0
    for var in REDIS_PASSWORD JWT_ACCESS_SECRET JWT_REFRESH_SECRET MINIO_ACCESS_KEY MINIO_SECRET_KEY; do
      if [[ -z "${!var:-}" ]]; then need_random=1; break; fi
    done
    if [[ "$need_random" == 1 ]]; then
      command -v openssl >/dev/null || { echo "需要 openssl 来生成随机密钥（或自行提供 REDIS_PASSWORD / JWT_* / MINIO_* 环境变量）" >&2; exit 1; }
    fi
    REDIS_PASSWORD="${REDIS_PASSWORD:-$(openssl rand -base64 24)}"
    JWT_ACCESS_SECRET="${JWT_ACCESS_SECRET:-$(openssl rand -base64 48)}"
    JWT_REFRESH_SECRET="${JWT_REFRESH_SECRET:-$(openssl rand -base64 48)}"
    MINIO_ACCESS_KEY="${MINIO_ACCESS_KEY:-$(openssl rand -hex 12)}"
    MINIO_SECRET_KEY="${MINIO_SECRET_KEY:-$(openssl rand -base64 32)}"
  fi

  log "将创建 Secret（8 个键；值不回显）："
  log "  CHUANGYUE_MYSQL_PASSWORD=<已提供>"
  log "  CHUANGYUE_INTERACTION_MYSQL_PASSWORD=<已提供>（必须等于 migrations/0015 的 CREATE USER 口令）"
  log "  CHUANGYUE_REDIS_PASSWORD / JWT_ACCESS_SECRET / JWT_REFRESH_SECRET / UPLOAD_MINIO_*=<生成或已提供>"
  if [[ -n "$MAIL_PASSWORD" ]]; then
    log "  CHUANGYUE_MAIL_PASSWORD=<已提供>"
  else
    log "  CHUANGYUE_MAIL_PASSWORD=<空，dev 用 log 驱动>"
  fi

  if confirm "创建 $SECRET_NAME？"; then
    if [[ "$DRY_RUN" == 1 ]]; then
      log "+ kubectl -n $K8S_NAMESPACE create secret generic $SECRET_NAME --from-literal=CHUANGYUE_MYSQL_PASSWORD=… （共 8 个 --from-literal）"
    else
      kubectl -n "$K8S_NAMESPACE" create secret generic "$SECRET_NAME" \
        --from-literal=CHUANGYUE_MYSQL_PASSWORD="$MYSQL_PASSWORD" \
        --from-literal=CHUANGYUE_INTERACTION_MYSQL_PASSWORD="$INTERACTION_MYSQL_PASSWORD" \
        --from-literal=CHUANGYUE_REDIS_PASSWORD="$REDIS_PASSWORD" \
        --from-literal=CHUANGYUE_JWT_ACCESS_SECRET="$JWT_ACCESS_SECRET" \
        --from-literal=CHUANGYUE_JWT_REFRESH_SECRET="$JWT_REFRESH_SECRET" \
        --from-literal=CHUANGYUE_UPLOAD_MINIO_ACCESS_KEY="$MINIO_ACCESS_KEY" \
        --from-literal=CHUANGYUE_UPLOAD_MINIO_SECRET_KEY="$MINIO_SECRET_KEY" \
        --from-literal=CHUANGYUE_MAIL_PASSWORD="$MAIL_PASSWORD"
      log "Secret 已创建"
    fi
  else
    log "已取消建 Secret；后续 MySQL 起不来（取不到 root 口令），请自行补建"
  fi
fi

# ---------- 阶段 3 · 起中间件（可选） ----------
step "3 · 中间件（自建）"
if [[ "$WITH_INFRA" == 1 ]]; then
  log "MySQL 的 root 口令取自 $SECRET_NAME 的 CHUANGYUE_MYSQL_PASSWORD —— 所以 Secret 必须先存在"
  if confirm "apply -k deploy/k8s/infra？"; then
    run kubectl apply -k "$REPO_ROOT/deploy/k8s/infra"
  fi
else
  log "未加 --with-infra，跳过（用云托管时本就不 apply infra，只要集群内存在同名 Service）"
fi

# ---------- 阶段 4 · 灌库 ----------
step "4 · 灌库（初始化表结构）"
if [[ "$SKIP_DB" == 1 ]]; then
  log "--skip-db：跳过"
else
  existing=""
  if [[ "$DRY_RUN" == 1 ]]; then
    log "（dry-run）假定 MySQL Pod $MYSQL_POD_NAME 已就绪，跳过就绪等待与「库是否非空」探测"
  else
    if ! k_exists "pod/$MYSQL_POD_NAME"; then
      log "找不到 Pod $MYSQL_POD_NAME —— 灌库需要**集群内**的自建 MySQL。加 --with-infra 起中间件，或 --skip-db 跳过"
      exit 1
    fi
    log "等 $MYSQL_POD_NAME 就绪（最长 300s）…"
    kubectl -n "$K8S_NAMESPACE" wait --for=condition=Ready "pod/$MYSQL_POD_NAME" --timeout=300s
    # 探测库是否已有表：migrations 里 0016 不幂等，重跑会中断，故非空默认拒灌。
    existing="$(mysql_in_pod chuangyue -N -B -e "SHOW TABLES LIKE 'user';" 2>/dev/null || true)"
  fi

  if [[ -n "$existing" && "$FORCE_DB" != 1 ]]; then
    log "库 chuangyue 已有表（例如 user）——默认不重灌，避免 0016 报 Duplicate column name。"
    log "存量库升级请照 deploy/k8s/README.md「初始化数据库」的升级段手动跑；确要重灌加 --force-db"
  elif confirm "按文件名顺序灌入 $REPO_ROOT/migrations/*.sql？"; then
    for f in "$REPO_ROOT"/migrations/*.sql; do
      log "==> $(basename "$f")"
      if [[ "$DRY_RUN" == 1 ]]; then
        log "+ kubectl -n $K8S_NAMESPACE exec -i $MYSQL_POD_NAME -- mysql -uroot … < $(basename "$f")"
      else
        mysql_in_pod "" < "$f"
      fi
    done
    log "灌库完成（0015 已建 chuangyue_interaction 库与独立账号）"
  else
    log "已取消灌库"
  fi
fi

# ---------- 阶段 5 · 提权管理员（可选） ----------
step "5 · 管理员账号（可选）"
if [[ -z "$ADMIN_USER" ]]; then
  log "未指定 --admin-user，跳过（站点注册后自行 UPDATE user SET role=1）"
elif [[ "$SKIP_DB" == 1 || "$DRY_RUN" == 1 ]]; then
  log "灌库未执行/dry-run：跳过提权"
else
  cnt="$(mysql_in_pod chuangyue -N -B -e "SELECT COUNT(*) FROM user WHERE username='$ADMIN_USER';" 2>/dev/null || echo 0)"
  if [[ "$cnt" == "0" ]]; then
    log "账号 '$ADMIN_USER' 不存在——请先在站点注册，再重跑 --admin-user $ADMIN_USER"
  else
    mysql_in_pod chuangyue -e "UPDATE user SET role=1 WHERE username='$ADMIN_USER';"
    log "已把 '$ADMIN_USER' 提权为管理员（重新登录后生效）"
  fi
fi

# ---------- 阶段 6 · 验收 ----------
step "6 · 验收"
if [[ "$DRY_RUN" == 1 || "$SKIP_DB" == 1 ]]; then
  log "未灌库/dry-run：跳过表核对"
else
  for db in chuangyue chuangyue_interaction; do
    n="$(mysql_in_pod "" -N -B -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='$db';" 2>/dev/null || echo '?')"
    log "$db 表数：$n"
  done
  # 互动库独立账号能否连（口令即 0015 里那个）
  if kubectl -n "$K8S_NAMESPACE" exec -i "$MYSQL_POD_NAME" -- \
       env MYSQL_PWD="$INTERACTION_MYSQL_PASSWORD" \
       mysql -uchy_interaction chuangyue_interaction -N -B \
       -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='chuangyue_interaction';" >/dev/null 2>&1; then
    log "chuangyue_interaction 独立账号 chy_interaction 连接成功"
  else
    log "⚠️ chy_interaction 连接失败——检查 INTERACTION_MYSQL_PASSWORD 是否等于 0015 里 CREATE USER 的口令"
  fi
fi

step "完成"
log "下一步：kubectl apply -k $REPO_ROOT/deploy/k8s/overlays/prod（或 dev），再 kubectl -n $K8S_NAMESPACE get pods -w"
if [[ "$DRY_RUN" == 1 ]]; then log "（本次为 dry-run，未对集群做任何改动）"; fi
exit 0
