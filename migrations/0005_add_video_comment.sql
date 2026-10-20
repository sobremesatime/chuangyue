-- 窗月 · 迁移 0005：新增视频评论表
-- 变更目的：支撑视频详情页的评论能力（发表、列表、删除自己的评论）。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
CREATE TABLE IF NOT EXISTS `video_comment` (
  `id`         BIGINT UNSIGNED NOT NULL               COMMENT '主键（雪花 ID）',
  `video_id`   BIGINT UNSIGNED NOT NULL               COMMENT '视频 ID',
  `user_id`    BIGINT UNSIGNED NOT NULL               COMMENT '评论者用户 ID',
  `content`    VARCHAR(1000)   NOT NULL               COMMENT '评论内容',
  `status`     TINYINT         NOT NULL DEFAULT 1     COMMENT '状态：1 正常 / 2 已删除（软删）',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_video_created` (`video_id`, `created_at`),
  KEY `idx_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='视频评论表';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `video_comment`;
