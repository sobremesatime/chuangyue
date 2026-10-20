-- 窗月 · 迁移 0009：新增「稍后再看」关系表
-- 变更目的：支撑用户把视频加入稍后再看列表（与收藏相互独立）。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
CREATE TABLE IF NOT EXISTS `video_watch_later` (
  `id`         BIGINT UNSIGNED NOT NULL               COMMENT '主键（雪花 ID）',
  `user_id`    BIGINT UNSIGNED NOT NULL               COMMENT '用户 ID',
  `video_id`   BIGINT UNSIGNED NOT NULL               COMMENT '视频 ID',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_video` (`user_id`, `video_id`),
  KEY `idx_video` (`video_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='稍后再看表';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `video_watch_later`;
