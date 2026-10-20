-- 窗月 · 迁移 0012：新增「举报」表
-- 变更目的：支撑内容治理——用户可对视频 / 评论发起举报，管理端按状态跟进处理。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
CREATE TABLE IF NOT EXISTS `video_report` (
  `id`          BIGINT UNSIGNED NOT NULL                COMMENT '主键（雪花 ID）',
  `user_id`     BIGINT UNSIGNED NOT NULL                COMMENT '举报人用户 ID',
  `target_type` TINYINT         NOT NULL                COMMENT '被举报对象类型：1 视频、2 评论',
  `target_id`   BIGINT UNSIGNED NOT NULL                COMMENT '被举报对象 ID',
  `video_id`    BIGINT UNSIGNED NOT NULL                COMMENT '相关视频 ID（管理端跳转定位用）',
  `reason`      VARCHAR(50)     NOT NULL                COMMENT '举报理由（取自白名单）',
  `detail`      VARCHAR(200)    NOT NULL DEFAULT ''     COMMENT '补充说明',
  `status`      TINYINT         NOT NULL DEFAULT 0      COMMENT '处理状态：0 待处理、1 已处理、2 已忽略',
  `handled_by`  BIGINT UNSIGNED NOT NULL DEFAULT 0      COMMENT '处理人用户 ID',
  `handled_at`  DATETIME        NULL                    COMMENT '处理时间',
  `created_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  -- 同一个人对同一对象只允许举报一次，避免重复刷举报
  UNIQUE KEY `uk_user_target` (`user_id`, `target_type`, `target_id`),
  KEY `idx_status_created` (`status`, `created_at`),
  KEY `idx_video` (`video_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='举报表';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `video_report`;
