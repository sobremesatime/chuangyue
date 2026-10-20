-- 窗月 · 迁移 0007：视频表新增收藏数
-- 变更目的：视频详情页需要展示被收藏的总数，与点赞数一样由视频表冗余一列，
--           避免每次展示都去 video_favorite 表做 COUNT。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
ALTER TABLE `video`
  ADD COLUMN `favorite_count` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '收藏数' AFTER `like_count`;

-- 存量数据回填：按关系表实际条数补齐，避免历史收藏统计为 0
UPDATE `video` v
  SET v.`favorite_count` = (
    SELECT COUNT(*) FROM `video_favorite` f WHERE f.`video_id` = v.`id`
  );

-- ----------------------------
-- 回滚
-- ----------------------------
-- ALTER TABLE `video` DROP COLUMN `favorite_count`;
