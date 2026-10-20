#!/usr/bin/env bash
#
# 窗月 · 集群内定时备份（K8s CronJob 用）
#
# 与 deploy/scripts/backup.sh 的关系：那一份是**宿主机侧**的，跑在能执行 docker / kubectl 的
# 机器上（docker 模式进容器取、k8s 模式 exec 进 Pod 取）。本脚本是**集群内**版本——备份 Pod
# 本身就挂在 MySQL 与上传卷旁边，因此不需要任何 docker / kubectl：直接连 `mysql` Service 导库、
# 直接读挂进来的上传卷打包。两条通道各自成型、不混用（「备份库成功、备份文件失败」这种半截结果
# 最难排查）。选哪条：有宿主 crontab 与 kubectl 就用那份；纯集群、不进宿主就用这份。
#
# 环境变量（在 CronJob 里注入；均有默认值。口令**只经 Secret 注入**，不落盘、不进命令行）：
#   MYSQL_HOST        MySQL 服务名（集群内 Service）  默认 mysql
#   MYSQL_PORT        MySQL 端口                    默认 3306
#   MYSQL_USER        账号                         默认 root
#   MYSQL_PASSWORD    口令                          **必填**（由 Secret 提供）
#   DB_LIST           要备份的库（空格分隔）          默认 "chuangyue chuangyue_interaction"
#   BACKUP_DIR        归档目录（PVC 挂载点）          默认 /var/backups/chuangyue
#   RETAIN_DAYS       归档保留天数                   默认 7
#   UPLOADS_DIR       上传卷挂载点                   默认 /uploads（不存在则跳过）
#
# 取舍（与宿主机版一致）：
# - **两个库都备**（chuangyue + chuangyue_interaction）：只备主库会漏掉点赞 / 评论 / 弹幕 / 通知。
# - **不备 Redis**：它只存结果缓存、限流计数与转码预约信号量，都能由业务与数据库重建；
#   把快照恢复回来反而会带出「幽灵预约」「过期限流键」这类脏状态。
# - **上传目录一起备**：转码产物可以重转，但用户上传的源文件丢了就找不回来。
# - **先写 .tmp 再原子 mv**：归档目录里出现的文件要么完整，要么根本不存在。
# - **保留期用纯 shell + `date -r` 判龄，不依赖 `find`**：精简镜像未必带 findutils，
#   而 GNU `date -r <file> +%s` 在基础镜像里就有，少一个外部依赖就少一种「镜像里没有」的意外。
# - **本脚本不负责异地化**：归档与数据同盘，盘坏掉时两者一起没——请再挂一步 rsync / ossutil
#   把 BACKUP_DIR 里的归档搬到对象存储（见 deploy/scripts/backup.sh 的同类说明）。
#
set -euo pipefail

MYSQL_HOST="${MYSQL_HOST:-mysql}"
MYSQL_PORT="${MYSQL_PORT:-3306}"
MYSQL_USER="${MYSQL_USER:-root}"
DB_LIST="${DB_LIST:-chuangyue chuangyue_interaction}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/chuangyue}"
RETAIN_DAYS="${RETAIN_DAYS:-7}"
UPLOADS_DIR="${UPLOADS_DIR:-/uploads}"

: "${MYSQL_PASSWORD:?必须用环境变量提供 MYSQL_PASSWORD（由 chuangyue-secrets 注入，不用命令行参数，避免口令出现在 ps / shell history 里）}"

# 启动即自检所需命令，早失败给出明确原因，而不是跑一半报「command not found」
for bin in mysqldump gzip tar; do
  command -v "$bin" >/dev/null || {
    echo "缺少 $bin：备份镜像需自带 mysql 客户端与 gzip / tar（默认用 mysql:8.0）" >&2
    exit 1
  }
done

STAMP="$(date +%Y%m%d-%H%M%S)"
log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }

# 从一个库导出：选项都是「为什么这么选」的取舍——
#   --single-transaction 在 InnoDB 上开一致性快照，**不锁表**，备份期间线上照常读写
#   --quick               逐行流式取，避免把整表塞进内存（大表会 OOM）
#   --no-tablespaces      不读表空间信息，从而不要求 PROCESS 权限（最小权限账号也能跑）
# 口令走 MYSQL_PWD 环境变量：客户端会读它，因此不会出现在进程命令行上（写成 -p'xxx' 会被 ps 看到）。
# 竖线失败（mysqldump 中途出错）经 pipefail 传导为非零退出码，不会产出「看似完整」的截断归档。
dump_db() {
  local db="$1"
  local out="$BACKUP_DIR/mysql/${db}-${STAMP}.sql.gz"
  local tmp="${out}.tmp"

  log "备份库 $db → $out"
  if ! MYSQL_PWD="$MYSQL_PASSWORD" mysqldump -h"$MYSQL_HOST" -P"$MYSQL_PORT" -u"$MYSQL_USER" \
        --single-transaction --quick --no-tablespaces \
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

# 打包上传目录：PVC readOnly 挂进来，直接 tar 挂载点即可，无需 exec 别的 Pod
archive_uploads() {
  if [[ ! -d "$UPLOADS_DIR" ]]; then
    log "未挂载上传卷（$UPLOADS_DIR 不存在），跳过上传文件备份"
    return 0
  fi

  local out="$BACKUP_DIR/uploads-${STAMP}.tar.gz"
  local tmp="${out}.tmp"

  log "打包上传目录 $UPLOADS_DIR → $out"
  if ! tar -C "$UPLOADS_DIR" -cf - . | gzip -c >"$tmp"; then
    rm -f "$tmp"
    log "错误：上传目录打包失败" >&2
    return 1
  fi
  if [[ ! -s "$tmp" ]]; then
    rm -f "$tmp"
    log "错误：上传目录归档为空" >&2
    return 1
  fi
  mv "$tmp" "$out"
}

# 保留期清理：只删本脚本自己归档出来的 .gz，以及中途失败留下的 .tmp。
# 用 glob + `date -r` 判龄（不依赖 find，见文件头）。
prune_old() {
  log "清理超过 ${RETAIN_DAYS} 天的归档"
  local now cutoff f mtime
  now="$(date +%s)"
  cutoff=$((now - RETAIN_DAYS * 86400))

  for f in "$BACKUP_DIR"/mysql/*.sql.gz "$BACKUP_DIR"/uploads-*.tar.gz "$BACKUP_DIR"/*.tmp "$BACKUP_DIR"/mysql/*.tmp; do
    [[ -e "$f" ]] || continue
    mtime="$(date -r "$f" +%s 2>/dev/null || echo 0)"
    if [[ "$mtime" -lt "$cutoff" ]]; then
      log "删除过期归档 $f"
      rm -f "$f"
    fi
  done
}

mkdir -p "$BACKUP_DIR/mysql"

for db in $DB_LIST; do
  dump_db "$db"
done
archive_uploads
prune_old

log "完成，当前归档："
ls -lh "$BACKUP_DIR" "$BACKUP_DIR/mysql"
