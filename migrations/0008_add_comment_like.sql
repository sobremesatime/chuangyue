-- 窗月 · 迁移 0008：评论点赞
-- 变更目的：评论支持点赞，与视频点赞同样的做法——
--           关系表记录「谁赞了哪条评论」，评论表冗余一列计数避免每次 COUNT。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
ALTER TABLE `video_comment`
  ADD COLUMN `like_count` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '点赞数' AFTER `content`;

CREATE TABLE IF NOT EXISTS `video_comment_like` (
  `id`         BIGINT UNSIGNED NOT NULL COMMENT '主键（雪花 ID）',
  `comment_id` BIGINT UNSIGNED NOT NULL COMMENT '评论 ID',
  `user_id`    BIGINT UNSIGNED NOT NULL COMMENT '点赞用户 ID',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_comment` (`user_id`, `comment_id`),
  KEY `idx_comment` (`comment_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='评论点赞关系表';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `video_comment_like`;
-- ALTER TABLE `video_comment` DROP COLUMN `like_count`;
