-- 窗月 · 迁移 0006：新增视频弹幕表
-- 变更目的：支撑播放器上的弹幕能力（发表、按时间轴拉取、删除）。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
CREATE TABLE IF NOT EXISTS `video_danmaku` (
  `id`         BIGINT UNSIGNED NOT NULL                    COMMENT '主键（雪花 ID）',
  `video_id`   BIGINT UNSIGNED NOT NULL                    COMMENT '视频 ID',
  `user_id`    BIGINT UNSIGNED NOT NULL                    COMMENT '发送者用户 ID',
  `content`    VARCHAR(100)    NOT NULL                    COMMENT '弹幕内容',
  `time_ms`    INT UNSIGNED    NOT NULL                    COMMENT '视频时间轴位置（毫秒）',
  `color`      VARCHAR(7)      NOT NULL DEFAULT '#FFFFFF'  COMMENT '文字颜色（十六进制）',
  `mode`       TINYINT         NOT NULL DEFAULT 1          COMMENT '展示模式：1 滚动 / 2 顶部 / 3 底部',
  `status`     TINYINT         NOT NULL DEFAULT 1          COMMENT '状态：1 正常 / 2 已删除（软删）',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_video_time` (`video_id`, `time_ms`),
  KEY `idx_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='视频弹幕表';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `video_danmaku`;
