-- 窗月 · 迁移 0011：视频评论支持回复（二级评论）
-- 变更目的：把单级评论升级为「一级评论 + 其下的回复」，回复统一扁平挂在一级评论下（不再嵌套第三层）。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
ALTER TABLE `video_comment`
  ADD COLUMN `parent_id`        BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '所属一级评论 ID，0 表示本身是一级评论' AFTER `user_id`,
  ADD COLUMN `reply_to_user_id` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '被回复者用户 ID（回复某条回复时用于 @ 展示），0 表示未指定' AFTER `parent_id`,
  ADD COLUMN `reply_count`      INT UNSIGNED    NOT NULL DEFAULT 0 COMMENT '一级评论的回复总数（冗余列，仅一级评论维护）' AFTER `like_count`,
  ADD KEY `idx_parent_created` (`parent_id`, `created_at`);

-- ----------------------------
-- 回滚
-- ----------------------------
-- ALTER TABLE `video_comment`
--   DROP KEY `idx_parent_created`,
--   DROP COLUMN `reply_count`,
--   DROP COLUMN `reply_to_user_id`,
--   DROP COLUMN `parent_id`;
