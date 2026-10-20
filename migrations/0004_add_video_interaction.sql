-- 窗月 · 迁移 0004：新增用户与视频之间的互动关系表
-- 变更目的：支撑点赞、收藏、观看历史三项用户侧沉淀能力。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
CREATE TABLE IF NOT EXISTS `video_like` (
  `id`         BIGINT UNSIGNED NOT NULL               COMMENT '主键（雪花 ID）',
  `user_id`    BIGINT UNSIGNED NOT NULL               COMMENT '用户 ID',
  `video_id`   BIGINT UNSIGNED NOT NULL               COMMENT '视频 ID',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_video` (`user_id`, `video_id`),
  KEY `idx_video` (`video_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='视频点赞表';

CREATE TABLE IF NOT EXISTS `video_favorite` (
  `id`         BIGINT UNSIGNED NOT NULL               COMMENT '主键（雪花 ID）',
  `user_id`    BIGINT UNSIGNED NOT NULL               COMMENT '用户 ID',
  `video_id`   BIGINT UNSIGNED NOT NULL               COMMENT '视频 ID',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_video` (`user_id`, `video_id`),
  KEY `idx_video` (`video_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='视频收藏表';

CREATE TABLE IF NOT EXISTS `video_watch_history` (
  `id`         BIGINT UNSIGNED NOT NULL               COMMENT '主键（雪花 ID）',
  `user_id`    BIGINT UNSIGNED NOT NULL               COMMENT '用户 ID',
  `video_id`   BIGINT UNSIGNED NOT NULL               COMMENT '视频 ID',
  `progress`   INT             NOT NULL DEFAULT 0     COMMENT '播放进度（秒）',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_video` (`user_id`, `video_id`),
  KEY `idx_user_updated` (`user_id`, `updated_at`),
  KEY `idx_video` (`video_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='观看历史表';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `video_watch_history`;
-- DROP TABLE IF EXISTS `video_favorite`;
-- DROP TABLE IF EXISTS `video_like`;
