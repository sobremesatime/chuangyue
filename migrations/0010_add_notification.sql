-- 窗月 · 迁移 0010：新增「通知」表
-- 变更目的：支撑顶栏消息中心的未读提醒——评论被点赞、上传的视频收到新评论时给接收者留一条通知。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
CREATE TABLE IF NOT EXISTS `notification` (
  `id`         BIGINT UNSIGNED NOT NULL                COMMENT '主键（雪花 ID）',
  `user_id`    BIGINT UNSIGNED NOT NULL                COMMENT '接收者用户 ID',
  `actor_id`   BIGINT UNSIGNED NOT NULL                COMMENT '触发者用户 ID',
  `type`       TINYINT         NOT NULL                COMMENT '类型：1 评论被点赞、2 视频收到评论',
  `target_id`  BIGINT UNSIGNED NOT NULL                COMMENT '目标 ID（评论 ID），兼作点击跳转锚点',
  `video_id`   BIGINT UNSIGNED NOT NULL                COMMENT '相关视频 ID，点击跳转用',
  `content`    VARCHAR(255)    NOT NULL DEFAULT ''     COMMENT '触发时的内容快照（评论正文，超长已截断）',
  `is_read`    TINYINT         NOT NULL DEFAULT 0      COMMENT '是否已读：0 未读、1 已读',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  -- 同一个人对同一条评论的点赞只提醒一次，取消再赞不会重复打扰
  UNIQUE KEY `uk_user_actor_type_target` (`user_id`, `actor_id`, `type`, `target_id`),
  KEY `idx_user_read_created` (`user_id`, `is_read`, `created_at`),
  KEY `idx_video` (`video_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='通知表';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `notification`;
