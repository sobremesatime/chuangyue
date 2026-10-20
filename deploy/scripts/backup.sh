#!/usr/bin/env bash
#
# 窗月 · 数据备份（MySQL 业务库 + 上传源文件 / HLS 产物）
#
# 为什么需要它：演示环境用的是自建 MySQL（不是云 RDS），没有自动备份；一次误删表、
# 一次磁盘损坏，数据就没了。本脚本把「数据库」与「不可再生的上传文件」定期落成
# 带时间戳的归档，并按保留期清理旧归档。
#
# 用法（建议放 crontab，在中间件所在机器上跑；口令只经环境变量传入）：
#   MYSQL_PASSWORD='xxx' ./backup.sh                     # 默认 docker 模式（本机容器）
#   MYSQL_PASSWORD='xxx' BACKUP_MODE=k8s ./backup.sh     # k3s 部署：把库与卷都从集群里取
#   # 每天 03:30 备份，日志另存
#   30 3 * * * MYSQL_PASSWORD='xxx' /opt/chuangyue/deploy/scripts/backup.sh >>/var/log/chuangyue-backup.log 2>&1
#
# 环境变量（均有默认值，按需覆盖）：
#   BACKUP_DIR        归档目录                     默认 /var/backups/chuangyue
#   RETAIN_DAYS       归档保留天数                 默认 7
#   BACKUP_MODE       docker | k8s | direct        默认 docker
#   MYSQL_PASSWORD    MySQL 口令                   **必填**（不落盘、不进命令行）
#   MYSQL_USER        MySQL 账号                   默认 root
#   DB_LIST           要备份的库（空格分隔）        默认 "chuangyue chuangyue_interaction"
#
#   —— docker 模式（默认；compose 或中间件跑在本机容器里）——
#   MYSQL_CONTAINER       MySQL 容器名             默认 chuangyue-mysql
#   UPLOADS_CONTAINER     挂着上传卷的容器名        默认 chuangyue-video（留空则跳过）
#   UPLOADS_DIR           容器内上传目录            默认 /app/uploads
#
#   —— k8s 模式（**需要你已获授权的 kubectl 访问**，脚本自身不申请任何权限）——
#   K8S_NAMESPACE         命名空间                  默认 chuangyue
#   MYSQL_POD_NAME        MySQL Pod 名              默认按标签 app=mysql 自动查找
#   UPLOADS_POD_NAME      挂着上传卷的 Pod 名        默认空 = 跳过上传文件备份
#
#   —— direct 模式（mysqldump / tar 装在宿主机上，直连）——
#   MYSQL_HOST / MYSQL_PORT                       默认 127.0.0.1:3306
#   UPLOADS_DIR           宿主机上传目录            默认 /app/uploads
#
# 三点刻意的取舍：
# - **每种模式只用自己那条通道**：docker 模式全程用 docker，k8s 模式全程用 kubectl，
#   绝不混用——否则「备份库成功、备份文件失败」这种半截结果最难排查。
# - **不备份 Redis**：它只存结果缓存、限流计数与转码预约信号量，都能由业务与数据库重建；
#   把 Redis 快照恢复回来反而会带出「幽灵预约」「过期限流键」这类脏状态。
# - **归档必须离开这台机器**：本脚本只负责在本机生成归档，不负责异地化。若备份与数据
#   躺在同一块盘上，盘坏掉时两者一起没——请再挂一步 rsync / ossutil 上传到对象存储。
#
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/var/backups/chuangyue}"
RETAIN_DAYS="${RETAIN_DAYS:-7}"
BACKUP_MODE="${BACKUP_MODE:-docker}"
MYSQL_USER="${MYSQL_USER:-root}"
DB_LIST="${DB_LIST:-chuangyue chuangyue_interaction}"

MYSQL_CONTAINER="${MYSQL_CONTAINER:-chuangyue-mysql}"
UPLOADS_CONTAINER="${UPLOADS_CONTAINER:-chuangyue-video}"

K8S_NAMESPACE="${K8S_NAMESPACE:-chuangyue}"
MYSQL_POD_NAME="${MYSQL_POD_NAME:-}"
UPLOADS_POD_NAME="${UPLOADS_POD_NAME:-}"

MYSQL_HOST="${MYSQL_HOST:-127.0.0.1}"
MYSQL_PORT="${MYSQL_PORT:-3306}"
UPLOADS_DIR="${UPLOADS_DIR:-/app/uploads}"

: "${MYSQL_PASSWORD:?必须用环境变量提供 MYSQL_PASSWORD（不用命令行参数，避免口令出现在 ps / shell history 里）}"

case "$BACKUP_MODE" in
  docker) command -v docker >/dev/null || { echo "docker 模式需要本机 docker 命令" >&2; exit 1; } ;;
  k8s)    command -v kubectl >/dev/null || { echo "k8s 模式需要 kubectl（且你已有相应集群访问授权）" >&2; exit 1; } ;;
  direct) command -v mysqldump >/dev/null || { echo "direct 模式需要本机 mysqldump" >&2; exit 1; } ;;
  *)      echo "未知 BACKUP_MODE=$BACKUP_MODE（可选 docker / k8s / direct）" >&2; exit 1 ;;
esac

STAMP="$(date +%Y%m%d-%H%M%S)"
log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }

# ---------- 数据库：一条通道，三种模式 ----------
#
# 口令统一用 MYSQL_PWD 传给客户端进程：mysqldump 会读这个环境变量，因此口令不会
# 出现在进程命令行上（写成 -p'xxx' 会被 ps 看到，也会落进 shell history）。
mysql_dump() {
  case "$BACKUP_MODE" in
    docker)
      docker exec -i -e "MYSQL_PWD=$MYSQL_PASSWORD" "$MYSQL_CONTAINER" \
        mysqldump -u"$MYSQL_USER" "$@"
      ;;
    k8s)
      local pod="$MYSQL_POD_NAME"
      if [[ -z "$pod" ]]; then
        pod="$(kubectl -n "$K8S_NAMESPACE" get pod -l app=mysql -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
      fi
      if [[ -z "$pod" ]]; then
        log "错误：在命名空间 $K8S_NAMESPACE 里找不到 MySQL Pod（可用 MYSQL_POD_NAME 显式指定）" >&2
        return 1
      fi
      # 不加 -i/-t：不接 stdin、不分配 TTY，纯单向取 stdout
      kubectl -n "$K8S_NAMESPACE" exec "$pod" -- \
        env "MYSQL_PWD=$MYSQL_PASSWORD" mysqldump -u"$MYSQL_USER" "$@"
      ;;
    direct)
      MYSQL_PWD="$MYSQL_PASSWORD" mysqldump -h"$MYSQL_HOST" -P"$MYSQL_PORT" -u"$MYSQL_USER" "$@"
      ;;
  esac
}

# 导出一个库。选项都是「为什么这么选」的取舍：
#   --single-transaction 在 InnoDB 上开一致性快照，**不锁表**，备份期间线上照常读写
#   --quick               逐行流式取，避免把整表塞进内存（大表会 OOM）
#   --no-tablespaces      不读表空间信息，从而不要求 PROCESS 权限（最小权限账号也能跑）
# 竖线失败（mysqldump 中途出错）经 pipefail 传导为非零退出码，不会产出「看似完整」的截断归档；
# 另外先写 .tmp 再 mv：出现在归档目录里的文件要么是完整的，要么根本不存在。
dump_db() {
  local db="$1"
  local out="$BACKUP_DIR/mysql/${db}-${STAMP}.sql.gz"
  local tmp="${out}.tmp"

  log "备份库 $db → $out"
  if ! mysql_dump --single-transaction --quick --no-tablespaces \
        --default-character-set=utf8mb4 --databases "$db" | gzip -c >"$tmp"; then
    rm -f "$tmp"
    log "错误：$db 导出失败" >&2
    return 1
  fi
  if [[ ! -s "$tmp" ]]; then
    rm -f "$tmp"
    log "错误：$db 导出结果为空" >&2
    return 1
  fi
  mv "$tmp" "$out"
}

# ---------- 上传目录：同样按模式走各自的通道 ----------
#
# 为什么值得备份：转码产物可以重转，但**用户上传的源文件丢了就找不回来了**。
# k8s 模式下若 PVC 是 RWX 或与 Pod 同节点，也可以改成在节点上直接 tar 挂载点，速度更快。
archive_uploads() {
  local out="$BACKUP_DIR/uploads-${STAMP}.tar.gz"
  local tmp="${out}.tmp"

  case "$BACKUP_MODE" in
    docker)
      if [[ -z "$UPLOADS_CONTAINER" ]]; then
        log "UPLOADS_CONTAINER 为空，跳过上传文件备份"
        return 0
      fi
      log "打包上传目录 $UPLOADS_DIR（容器 $UPLOADS_CONTAINER）→ $out"
      if ! docker exec "$UPLOADS_CONTAINER" tar -C "$UPLOADS_DIR" -cf - . | gzip -c >"$tmp"; then
        rm -f "$tmp"; log "错误：上传目录打包失败" >&2; return 1
      fi
      ;;
    k8s)
      if [[ -z "$UPLOADS_POD_NAME" ]]; then
        log "未指定 UPLOADS_POD_NAME，跳过上传文件备份（建议指定挂着 chuangyue-uploads 卷的 Pod）"
        return 0
      fi
      log "打包上传目录 $UPLOADS_DIR（Pod $UPLOADS_POD_NAME）→ $out"
      # 不加 -i/-t：纯单向取 stdout 的二进制 tar 流
      if ! kubectl -n "$K8S_NAMESPACE" exec "$UPLOADS_POD_NAME" -- tar -C "$UPLOADS_DIR" -cf - . | gzip -c >"$tmp"; then
        rm -f "$tmp"; log "错误：上传目录打包失败" >&2; return 1
      fi
      ;;
    direct)
      if [[ ! -d "$UPLOADS_DIR" ]]; then
        log "宿主机目录 $UPLOADS_DIR 不存在，跳过上传文件备份（可用 UPLOADS_DIR 指定实际路径）"
        return 0
      fi
      log "打包上传目录 $UPLOADS_DIR → $out"
      if ! tar -C "$UPLOADS_DIR" -cf - . | gzip -c >"$tmp"; then
        rm -f "$tmp"; log "错误：上传目录打包失败" >&2; return 1
      fi
      ;;
  esac

  if [[ ! -s "$tmp" ]]; then
    rm -f "$tmp"
    log "错误：上传目录归档为空" >&2
    return 1
  fi
  mv "$tmp" "$out"
}

# 保留期清理：只删本脚本自己归档出来的 .gz，以及中途失败留下的 .tmp
prune_old() {
  log "清理超过 ${RETAIN_DAYS} 天的归档"
  find "$BACKUP_DIR" -type f \( -name '*.sql.gz' -o -name '*.tar.gz' -o -name '*.tmp' \) \
    -mtime "+${RETAIN_DAYS}" -print -delete
}

mkdir -p "$BACKUP_DIR/mysql"

for db in $DB_LIST; do
  dump_db "$db"
done
archive_uploads
prune_old

log "完成，当前归档："
ls -lh "$BACKUP_DIR" "$BACKUP_DIR/mysql"
