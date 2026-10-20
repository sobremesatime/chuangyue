-- 窗月 · 初始化迁移
-- 当前版本：用户表 + 视频表
-- 说明：MySQL 首次启动时会自动执行本目录下的脚本（见 deploy/compose/docker-compose.yml）

-- ----------------------------
-- 用户表
-- ----------------------------
CREATE TABLE IF NOT EXISTS `user` (
  `id`            BIGINT UNSIGNED NOT NULL                COMMENT '用户 ID（雪花 ID）',
  `username`      VARCHAR(32)     NOT NULL                COMMENT '用户名，登录用',
  `password_hash` VARCHAR(100)    NOT NULL                COMMENT 'bcrypt 密码哈希',
  `nickname`      VARCHAR(32)     NOT NULL DEFAULT ''     COMMENT '昵称',
  `avatar`        VARCHAR(255)    NOT NULL DEFAULT ''     COMMENT '头像地址',
  `role`          TINYINT         NOT NULL DEFAULT 0      COMMENT '角色：0 普通用户，1 管理员',
  `status`        TINYINT         NOT NULL DEFAULT 1      COMMENT '状态：1 正常，0 禁用',
  `created_at`    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_username` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户表';

-- ----------------------------
-- 视频表
-- ----------------------------
CREATE TABLE IF NOT EXISTS `video` (
  `id`          BIGINT UNSIGNED NOT NULL                COMMENT '视频 ID（雪花 ID）',
  `title`       VARCHAR(128)    NOT NULL                COMMENT '标题',
  `description` VARCHAR(512)    NOT NULL DEFAULT ''     COMMENT '简介',
  `cover_url`   VARCHAR(255)    NOT NULL DEFAULT ''     COMMENT '封面地址',
  `play_url`    VARCHAR(255)    NOT NULL DEFAULT ''     COMMENT '播放地址',
  `duration`    INT             NOT NULL DEFAULT 0      COMMENT '时长（秒）',
  `category_id` BIGINT UNSIGNED NOT NULL DEFAULT 0      COMMENT '分类 ID',
  `uploader_id` BIGINT UNSIGNED NOT NULL                COMMENT '上传者（管理员）ID',
  `status`      TINYINT         NOT NULL DEFAULT 0      COMMENT '状态：0 转码中，1 已上架，2 已下架',
  `view_count`  BIGINT UNSIGNED NOT NULL DEFAULT 0      COMMENT '播放量',
  `like_count`  BIGINT UNSIGNED NOT NULL DEFAULT 0      COMMENT '点赞数',
  `created_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_status_created` (`status`, `created_at`),
  KEY `idx_category` (`category_id`),
  KEY `idx_uploader` (`uploader_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='视频表';
