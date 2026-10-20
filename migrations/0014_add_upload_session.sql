-- 窗月 · 迁移 0014：分片上传会话与分片明细
-- 变更目的：大体积视频支持分片上传、断点续传与秒传（同文件复用）。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
CREATE TABLE IF NOT EXISTS `upload_session` (
  `id`           BIGINT UNSIGNED NOT NULL                COMMENT '会话 ID（雪花 ID，即对外的 upload_id）',
  `user_id`      BIGINT UNSIGNED NOT NULL                COMMENT '发起上传的管理员 ID',
  `file_md5`     CHAR(32)        NOT NULL                COMMENT '整文件 MD5，秒传索引',
  `filename`     VARCHAR(255)    NOT NULL                COMMENT '原始文件名',
  `content_type` VARCHAR(128)    NOT NULL DEFAULT ''     COMMENT 'MIME 类型',
  `title`        VARCHAR(128)    NOT NULL DEFAULT ''     COMMENT '视频标题（init 时确定，complete 时建视频用）',
  `description`  VARCHAR(512)    NOT NULL DEFAULT ''     COMMENT '视频简介',
  `category_id`  BIGINT UNSIGNED NOT NULL DEFAULT 0      COMMENT '分类 ID',
  `total_size`   BIGINT UNSIGNED NOT NULL                COMMENT '文件总字节数',
  `chunk_size`   BIGINT UNSIGNED NOT NULL                COMMENT '单片字节数',
  `part_total`   INT             NOT NULL                COMMENT '分片总数',
  `storage_key`  VARCHAR(512)    NOT NULL                COMMENT '对象 key',
  `driver`       VARCHAR(16)     NOT NULL                COMMENT '存储驱动：local / minio',
  `upload_id`    VARCHAR(512)    NOT NULL DEFAULT ''     COMMENT '驱动侧 uploadID',
  `status`       TINYINT         NOT NULL DEFAULT 1      COMMENT '状态：1 上传中，2 已完成，3 已中止',
  `video_id`     BIGINT UNSIGNED NULL                    COMMENT 'complete 成功后关联的视频 ID',
  `expires_at`   DATETIME        NOT NULL                COMMENT '过期时间，供清理任务回收未完成会话',
  `created_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_md5_status` (`file_md5`, `status`),
  KEY `idx_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分片上传会话';

CREATE TABLE IF NOT EXISTS `upload_part` (
  `session_id`  BIGINT UNSIGNED NOT NULL                COMMENT '所属会话 ID',
  `part_number` INT             NOT NULL                COMMENT '分片序号，从 1 开始',
  `etag`        VARCHAR(128)    NOT NULL DEFAULT ''     COMMENT '分片校验值',
  `size`        BIGINT UNSIGNED NOT NULL DEFAULT 0      COMMENT '分片字节数',
  `created_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`session_id`, `part_number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分片上传明细';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `upload_part`;
-- DROP TABLE IF EXISTS `upload_session`;